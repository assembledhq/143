package providers

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/nodetoolchain"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"golang.org/x/sync/semaphore"
)

type npmSetupManifest struct {
	Files       map[string]string `json:"files"`
	Fingerprint string            `json:"fingerprint"`
	Ready       bool              `json:"ready"`
	Outputs     []string          `json:"outputs"`
}

type npmSetupCredential struct {
	Registry string `json:"registry"`
	Scope    string `json:"scope"`
	Token    string `json:"token"`
}

var _ agent.NPMDependencySetupProvider = (*DockerProvider)(nil)

func (d *DockerProvider) PrepareNPMDependencies(ctx context.Context, sb *agent.Sandbox, targets []repoconfig.NPMSetup, revision string, resolve func(context.Context) ([]models.ResolvedPackageRegistryCredential, error)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	lock, _ := d.packageSetupLocks.LoadOrStore(sb.ID+":"+sb.WorkDir, semaphore.NewWeighted(1))
	guard := lock.(*semaphore.Weighted)
	if err := guard.Acquire(ctx, 1); err != nil {
		return "", err
	}
	defer guard.Release(1)
	inspect, err := d.client.ContainerInspect(ctx, sb.ID)
	if err != nil || inspect.ContainerJSONBase == nil || inspect.Image == "" {
		return "", errors.New("npm setup sandbox image unavailable")
	}
	control := map[string]any{"work_dir": sb.WorkDir, "targets": targets, "revision": revision, "image": inspect.Image}
	profile, err := npmSetupNetworkFromSandbox(inspect)
	if err != nil {
		return "", err
	}
	manifest, err := d.npmSetupProbe(ctx, sb, control)
	if err != nil {
		return "", err
	}
	toolchain, err := selectNPMToolchain(manifest.Files, targets)
	if err != nil {
		return "", err
	}
	control["toolchain"] = toolchain
	manifest, err = d.npmSetupProbe(ctx, sb, control)
	if err != nil {
		return "", err
	}
	confirmed, selectErr := selectNPMToolchain(manifest.Files, targets)
	if selectErr != nil {
		return "", selectErr
	}
	if confirmed != toolchain {
		return "", errors.New("npm setup inputs changed during toolchain selection")
	}
	if manifest.Ready {
		applyNPMToolchain(sb, toolchain)
		return manifest.Fingerprint, nil
	}
	credentials, err := resolve(ctx)
	if err != nil {
		return "", err
	}
	private := make([]npmSetupCredential, len(credentials))
	for i, credential := range credentials {
		private[i] = npmSetupCredential{credential.Metadata.RegistryURL, credential.Metadata.PackageScope, credential.Token}
	}
	archive, err := d.runIsolatedNPMSetup(ctx, sb, inspect.Image, targets, manifest.Files, private, profile)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := os.Remove(archive.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			d.logger.Warn().Msg("npm setup output temporary file removal failed")
		}
		if err := archive.Close(); err != nil {
			d.logger.Warn().Msg("npm setup output temporary file close failed")
		}
	}()
	if err := validateNPMSetupArchive(archive, manifest.Files); err != nil {
		return "", err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", errors.New("npm setup output unavailable")
	}
	control["previous_outputs"] = manifest.Outputs
	control["import"] = true
	encoded, err := json.Marshal(control)
	if err != nil {
		return "", errors.New("npm setup control unavailable")
	}
	code, execErr := d.npmSetupMainExec(ctx, sb, npmSetupImportPython, string(encoded), archive, io.Discard)
	if execErr != nil || code != 0 {
		return "", errors.New("npm dependency output could not be published")
	}
	delete(control, "import")
	control["stamp"] = manifest.Fingerprint
	stamped, err := d.npmSetupProbe(ctx, sb, control)
	if err != nil {
		return "", err
	}
	if !stamped.Ready || stamped.Fingerprint != manifest.Fingerprint {
		return "", errors.New("npm setup inputs changed during installation")
	}
	applyNPMToolchain(sb, toolchain)
	return manifest.Fingerprint, nil
}

func selectNPMToolchain(files map[string]string, targets []repoconfig.NPMSetup) (nodetoolchain.Toolchain, error) {
	directories := make([]string, len(targets))
	for i, target := range targets {
		directories[i] = target.Directory
	}
	toolchain, err := nodetoolchain.Select(files, directories)
	if err != nil {
		return nodetoolchain.Toolchain{}, &agent.NPMSetupError{Code: "EBADENGINE", Category: "permanent"}
	}
	return toolchain, nil
}

func applyNPMToolchain(sb *agent.Sandbox, toolchain nodetoolchain.Toolchain) {
	env := make(map[string]string, len(sb.Env)+1)
	for key, value := range sb.Env {
		env[key] = value
	}
	sb.NPMToolchainIdentity = "node@" + toolchain.NodeVersion + "/npm@" + toolchain.NPMVersion
	existing := sb.Env["PATH"]
	if existing == "" {
		existing = "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
	}
	paths := []string{toolchain.BinDir()}
	for _, entry := range strings.Split(existing, ":") {
		if entry != "" && !strings.HasPrefix(entry, "/opt/143/node/") {
			paths = append(paths, entry)
		}
	}
	env["PATH"] = strings.Join(paths, ":")
	sb.Env = env
}

func npmSetupSandbox(sb *agent.Sandbox) agent.Sandbox {
	clean := *sb
	clean.Env = map[string]string{"PATH": "/usr/bin:/bin", "PYTHONPATH": "", "PYTHONHOME": ""}
	return clean
}

func (d *DockerProvider) npmSetupProbe(ctx context.Context, sb *agent.Sandbox, control map[string]any) (npmSetupManifest, error) {
	raw, err := json.Marshal(control)
	if err != nil {
		return npmSetupManifest{}, errors.New("npm setup control unavailable")
	}
	var out bytes.Buffer
	code, execErr := d.npmSetupMainExec(ctx, sb, npmSetupProbePython, string(raw), nil, &out)
	if execErr != nil || code != 0 {
		return npmSetupManifest{}, errors.New("npm setup requires safe manifest files, package-lock v2 or v3, and registry or workspace dependencies")
	}
	if out.Len() > 24*1024*1024 {
		return npmSetupManifest{}, errors.New("npm setup manifests exceed capacity")
	}
	var result npmSetupManifest
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Fingerprint == "" {
		return npmSetupManifest{}, errors.New("npm setup manifest response invalid")
	}
	return result, nil
}

func (d *DockerProvider) runIsolatedNPMSetup(ctx context.Context, sb *agent.Sandbox, image string, targets []repoconfig.NPMSetup, files map[string]string, credentials []npmSetupCredential, profiles ...npmSetupNetworkProfile) (result *os.File, returnErr error) {
	toolchain, err := selectNPMToolchain(files, targets)
	if err != nil {
		return nil, err
	}
	pids := int64(128)
	profile := npmSetupNetworkProfile{Network: d.network, ResolvConf: d.resolvConf}
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	cfg := &container.Config{Image: image, User: "0:0", WorkingDir: "/", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"600"}, Env: []string{"PATH=/usr/bin:/bin", "HOME=/private/home", "TMPDIR=/private/tmp"}, Labels: map[string]string{SandboxLabelManaged: "true", SandboxLabelType: "package_setup", SandboxLabelOrgID: sb.OrgID, SandboxLabelSessionID: sb.SessionID, SandboxLabelCreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}
	host := &container.HostConfig{Runtime: d.runtime, NetworkMode: container.NetworkMode(profile.Network), AutoRemove: true, ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Resources: container.Resources{Memory: 4 * 1024 * 1024 * 1024, NanoCPUs: 2e9, PidsLimit: &pids}, Tmpfs: map[string]string{"/private": "rw,noexec,nosuid,nodev,size=1073741824,mode=0700", "/setup": "rw,noexec,nosuid,nodev,size=3221225472,mode=0700"}, LogConfig: container.LogConfig{Type: "none"}}
	if profile.ResolvConf != "" {
		host.Mounts = []mount.Mount{{Type: mount.TypeBind, Source: profile.ResolvConf, Target: "/etc/resolv.conf", ReadOnly: true}}
	} else {
		host.DNS = append([]string(nil), profile.DNS...)
	}
	created, err := d.client.ContainerCreate(ctx, cfg, host, nil, nil, "")
	if err != nil {
		return nil, errors.New("isolated npm setup container unavailable")
	}
	removed := false
	remove := func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := d.client.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			return errors.New("isolated npm setup cleanup failed")
		}
		removed = true
		return nil
	}
	defer func() {
		if !removed {
			returnErr = errors.Join(returnErr, remove())
		}
		if returnErr != nil && result != nil {
			name := result.Name()
			if err := result.Close(); err != nil {
				d.logger.Warn().Msg("failed npm setup output close failed")
			}
			if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				d.logger.Warn().Msg("failed npm setup output removal failed")
			}
			result = nil
		}
	}()
	if err := d.client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return nil, errors.New("isolated npm setup container could not start")
	}
	raw, err := json.Marshal(struct {
		Files       map[string]string       `json:"files"`
		Targets     []repoconfig.NPMSetup   `json:"targets"`
		Credentials []npmSetupCredential    `json:"credentials"`
		Toolchain   nodetoolchain.Toolchain `json:"toolchain"`
	}{files, targets, credentials, toolchain})
	if err != nil {
		return nil, errors.New("isolated npm setup input unavailable")
	}
	isolated := &agent.Sandbox{ID: created.ID, WorkDir: "/", Env: map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/private/home", "TMPDIR": "/private/tmp"}}
	command := "/usr/bin/python3 -I -S -c '" + shellEscape(npmSetupInstallPython) + "'"
	var diagnostics bytes.Buffer
	diagnosticWriter := &npmSetupBoundedWriter{writer: &diagnostics, remaining: 4096}
	code, err := d.ExecWithStdinForReviewPreparation(ctx, isolated, command, bytes.NewReader(raw), diagnosticWriter, io.Discard)
	clear(raw)
	if err != nil {
		return nil, errors.New("isolated npm dependency installation failed")
	}
	if code != 0 {
		failure := parseNPMSetupFailure(diagnostics.Bytes())
		var diagnostic *agent.NPMSetupError
		if errors.As(failure, &diagnostic) {
			d.logger.Warn().Str("code", diagnostic.Code).Str("node_version", diagnostic.NodeVersion).Str("npm_version", diagnostic.NPMVersion).Int64("install_duration_ms", diagnostic.PhaseDurationsMS["install"]).Msg("npm dependency setup rejected project inputs")
		}
		return nil, failure
	}
	output, err := os.CreateTemp("", "143-npm-output-*.tar")
	if err != nil {
		return nil, errors.New("npm dependency output staging unavailable")
	}
	result = output
	writer := &npmSetupBoundedWriter{writer: output, remaining: 2 * 1024 * 1024 * 1024}
	code, err = d.ExecForReviewPreparation(ctx, isolated, "/usr/bin/python3 -I -S -c '"+shellEscape(npmSetupExportPython)+"'", writer, io.Discard)
	if err != nil || code != 0 {
		return result, errors.New("npm dependency output export failed")
	}
	if err := remove(); err != nil {
		return result, err
	}
	return result, nil
}

// parseNPMSetupFailure validates every exported field independently of the installer.
func parseNPMSetupFailure(raw []byte) error {
	generic := errors.New("isolated npm dependency installation failed")
	if len(raw) > 4096 {
		return generic
	}
	var diagnostic agent.NPMSetupError
	if json.Unmarshal(raw, &diagnostic) != nil || !agent.IsTerminalNPMSetupError(&diagnostic) {
		return generic
	}
	version := regexp.MustCompile(`^v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)
	if (diagnostic.NodeVersion != "" && !version.MatchString(diagnostic.NodeVersion)) || (diagnostic.NPMVersion != "" && !version.MatchString(diagnostic.NPMVersion)) {
		return generic
	}
	for phase, duration := range diagnostic.PhaseDurationsMS {
		if phase != "install" || duration < 0 || duration > 600000 {
			return generic
		}
	}
	return &diagnostic
}

type npmSetupBoundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *npmSetupBoundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("npm setup output exceeds capacity")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func validateNPMSetupArchive(file *os.File, files map[string]string) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return errors.New("npm setup output unavailable")
	}
	reader := tar.NewReader(file)
	count := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("npm setup output archive invalid")
		}
		count++
		if count > 500000 || header.Size > 1024*1024*1024 {
			return errors.New("npm setup output exceeds capacity")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") {
			return errors.New("npm setup output path invalid")
		}
		parts := strings.Split(name, "/")
		index := -1
		for i, part := range parts {
			if part == "node_modules" {
				index = i
				break
			}
		}
		if index < 0 {
			return errors.New("npm setup output contains non-dependency files")
		}
		parent := path.Join(parts[:index]...)
		if parent == "" {
			parent = "."
		}
		if _, ok := files[path.Join(parent, "package.json")]; !ok {
			return errors.New("npm setup output parent has no manifest")
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink:
			if path.IsAbs(header.Linkname) {
				return errors.New("npm setup output link escapes repository")
			}
			// The archive name is canonical and relative. Resolve link components
			// from its parent without allowing a step above the repository root.
			depth := len(parts) - 1
			for _, component := range strings.Split(header.Linkname, "/") {
				switch component {
				case "", ".":
				case "..":
					depth--
				default:
					depth++
				}
				if depth < 0 {
					return errors.New("npm setup output link escapes repository")
				}
			}
		default:
			return errors.New("npm setup output contains unsupported files")
		}
	}
	if count == 0 {
		return errors.New("npm setup output is empty")
	}
	return nil
}

const npmSetupProbePython = `
import hashlib,json,os,pathlib,stat,sys
p=json.loads(sys.argv[1]);root=pathlib.Path(p['work_dir'])
if not root.is_absolute() or root.resolve()!=root: raise RuntimeError('unsafe workspace')
files={};total=0;managed_parents=set()
for target in p['targets']:
 directory=target['directory'];relative=pathlib.PurePosixPath(directory)
 managed_parents.add(relative.as_posix())
 if not directory or relative.is_absolute() or '..' in relative.parts or relative.as_posix()!=directory: raise RuntimeError('unsafe directory')
 base=root/directory
 if base.resolve()!=base or not base.is_dir(): raise RuntimeError('unsafe directory')
 for current,dirs,names in os.walk(base,followlinks=False):
  dirs[:]=sorted(d for d in dirs if d not in {'node_modules','.git','.143'} and not pathlib.Path(current,d).is_symlink())
  for name in ('package.json','package-lock.json'):
   if name not in names: continue
   f=pathlib.Path(current,name);relative=f.relative_to(root).as_posix()
   if relative in files: continue
   fd=os.open(f,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
   with os.fdopen(fd,'rb') as stream:
    info=os.fstat(stream.fileno())
    if not stat.S_ISREG(info.st_mode) or info.st_size>4*1024*1024: raise RuntimeError('unsafe manifest')
    raw=stream.read(4*1024*1024+1)
   total+=len(raw)
   if len(files)>=512 or total>16*1024*1024: raise RuntimeError('manifest capacity')
   files[relative]=raw.decode()
 manifest=json.loads(files[(pathlib.PurePosixPath(directory)/'package.json').as_posix()])
 lock=json.loads(files[(pathlib.PurePosixPath(directory)/'package-lock.json').as_posix()])
 if lock.get('lockfileVersion') not in (2,3) or not isinstance(lock.get('packages'),dict): raise RuntimeError('lock version')
 for name,entry in lock['packages'].items():
  if entry.get('link'):
   resolved=entry.get('resolved','')
   candidate=pathlib.PurePosixPath(directory)/resolved/'package.json'
   if '..' in candidate.parts or candidate.is_absolute() or candidate.as_posix() not in files: raise RuntimeError('unsafe workspace link')
   managed_parents.add(candidate.parent.as_posix())
  elif entry.get('resolved'):
   from urllib.parse import urlparse
   url=urlparse(entry['resolved'])
   if url.scheme!='https' or not url.hostname or url.username or url.password: raise RuntimeError('unsupported dependency transport')
 for relative,raw in files.items():
  if not relative.endswith('package.json') or pathlib.PurePosixPath(relative).parent.as_posix() not in managed_parents: continue
  package=json.loads(raw)
  for field in ('dependencies','devDependencies','optionalDependencies'):
   for spec in package.get(field,{}).values():
    if not isinstance(spec,str) or spec.startswith(('file:','link:','git:','git+','http:','https:','ssh:','github:')) or '/' in spec and not spec.startswith(('npm:','@')): raise RuntimeError('unsupported dependency')
# Capture selectors only at the repository and explicit setup-target roots.
for directory in sorted(set(['.']+[t['directory'] for t in p['targets']])):
 for name in ('.nvmrc','.node-version','package.json'):
  f=root/directory/name;relative=f.relative_to(root).as_posix()
  if relative in files or not os.path.lexists(f): continue
  fd=os.open(f,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
  with os.fdopen(fd,'rb') as stream:
   info=os.fstat(stream.fileno());limit=4096 if name!='package.json' else 4*1024*1024
   if not stat.S_ISREG(info.st_mode) or info.st_size>limit: raise RuntimeError('unsafe selector')
   raw=stream.read(limit+1)
  total+=len(raw)
  if len(raw)>limit or len(files)>=512 or total>16*1024*1024: raise RuntimeError('selector capacity')
  files[relative]=raw.decode()
canonical=json.dumps({'files':files,'targets':p['targets'],'revision':p['revision'],'image':p['image'],'toolchain':p.get('toolchain')},sort_keys=True,separators=(',',':'))
key=hashlib.sha256(canonical.encode()).hexdigest()
receipt_directory=root/'.git'
if not receipt_directory.is_dir() or receipt_directory.is_symlink():
 receipt_directory=pathlib.Path('/tmp/143-npm-receipts')
 receipt_directory.mkdir(mode=0o700,exist_ok=True)
 if receipt_directory.is_symlink() or receipt_directory.resolve()!=receipt_directory: raise RuntimeError('unsafe metadata')
receipt=receipt_directory/(hashlib.sha256(str(root).encode()).hexdigest()+'.json')
previous={}
if receipt.exists() or receipt.is_symlink():
 fd=os.open(receipt,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
 with os.fdopen(fd) as stream:
  info=os.fstat(stream.fileno())
  if not stat.S_ISREG(info.st_mode) or info.st_size>65536: raise RuntimeError('unsafe receipt')
  try: previous=json.load(stream)
  except ValueError: pass
outputs=previous.get('outputs',[]) if isinstance(previous,dict) else []
def safe_outputs():
 if not outputs: return False
 for output in outputs:
  parts=pathlib.PurePosixPath(output).parts
  if not parts or parts[-1]!='node_modules' or '..' in parts or pathlib.PurePosixPath(output).is_absolute(): return False
  target=root/output
  if not target.is_dir() or target.is_symlink() or target.resolve()!=target: return False
  marker=target/'.143-package-setup'
  if not marker.is_file() or marker.is_symlink() or marker.stat().st_size!=64 or marker.read_text()!=key: return False
 return True
if p.get('stamp'):
 if p['stamp']!=key: raise RuntimeError('inputs changed')
 outputs=[]
 for current,dirs,names in os.walk(root,followlinks=False):
  dirs[:]=sorted(d for d in dirs if d not in {'.git','.143'} and not pathlib.Path(current,d).is_symlink())
  if 'node_modules' in dirs:
   folder=pathlib.Path(current,'node_modules');relative=folder.relative_to(root).as_posix()
   if pathlib.PurePosixPath(relative).parent.as_posix() in managed_parents:
    outputs.append(relative)
    marker=folder/'.143-package-setup'
    fd=os.open(marker,os.O_WRONLY|os.O_CREAT|os.O_TRUNC|os.O_NOFOLLOW|os.O_NONBLOCK,0o600)
    with os.fdopen(fd,'w') as stream: stream.write(key)
   dirs.remove('node_modules')
 import tempfile
 fd,temp=tempfile.mkstemp(prefix='143-npm-receipt-',dir=receipt_directory)
 try:
  with os.fdopen(fd,'w') as stream: json.dump({'fingerprint':key,'outputs':outputs},stream)
  os.replace(temp,receipt)
 finally:
  if os.path.exists(temp): os.unlink(temp)
 previous={'fingerprint':key}
existing=[]
for parent in managed_parents:
 target=root/parent/'node_modules'
 if os.path.lexists(target):
  if target.parent.resolve()!=target.parent: raise RuntimeError('unsafe output parent')
  existing.append(target.relative_to(root).as_posix())
print(json.dumps({'files':files,'fingerprint':key,'ready':previous.get('fingerprint')==key and safe_outputs(),'outputs':sorted(set(outputs+existing))}))
`

const npmSetupInstallPython = `
import json,os,pathlib,re,shutil,subprocess,sys,time
from urllib.parse import urlparse
p=json.load(sys.stdin);root=pathlib.Path('/setup/project');root.mkdir()
for relative,content in p['files'].items():
 path=pathlib.PurePosixPath(relative)
 if path.is_absolute() or '..' in path.parts or path.name not in {'package.json','package-lock.json','.nvmrc','.node-version'}: raise RuntimeError('invalid manifest')
 target=root/path;target.parent.mkdir(parents=True,exist_ok=True);target.write_text(content)
toolchain=p['toolchain']
# Match catalog by exact versions, never accept paths supplied by repository files.
pair=(toolchain.get('node_version'),toolchain.get('npm_version'))
if any(not isinstance(v,str) or not re.fullmatch(r'[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}',v) for v in pair): raise RuntimeError('unsupported toolchain')
node_version,npm_version=pair
bin_dir='/opt/143/node/'+node_version+'/bin'
node=bin_dir+'/node';npm='/opt/143/node/'+node_version+'/lib/node_modules/npm/bin/npm-cli.js'
environment={'PATH':bin_dir+':/usr/bin:/bin','HOME':'/private/home','TMPDIR':'/private/tmp'}
def version(command):
 try:
  value=subprocess.check_output(command,stderr=subprocess.DEVNULL,env=environment,timeout=10).decode().strip()
  return value if re.fullmatch(r'v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}',value) else ''
 except Exception: return ''
if version([node,'--version'])!='v'+node_version or version([node,npm,'--version'])!=npm_version:
 print(json.dumps({'code':'EBADENGINE','category':'permanent'}),flush=True);sys.exit(1)
private=pathlib.Path('/private');(private/'home').mkdir();(private/'tmp').mkdir()
config=private/'npmrc';lines=[];registries=set();scopes=set()
for credential in p.pop('credentials') or []:
 registry=credential['registry'];scope=credential['scope'];token=credential['token'];url=urlparse(registry)
 if url.scheme!='https' or not url.hostname or url.username or url.password or url.path!='/' or url.query or url.fragment or url.port not in (None,443) or not re.fullmatch(r'@[a-z0-9][a-z0-9._-]*',scope) or not token or any(c.isspace() or ord(c)<32 for c in token): raise RuntimeError('invalid credential')
 if registry in registries or scope in scopes: raise RuntimeError('duplicate credential binding')
 registries.add(registry);scopes.add(scope)
 lines.extend([scope+':registry='+registry,'//'+url.netloc+'/:_authToken='+token])
fd=os.open(config,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
with os.fdopen(fd,'w') as stream: stream.write('\n'.join(lines)+'\n')
del lines
started=time.monotonic();failure=None
try:
 for target in p['targets']:
  args=[node,npm,'ci','--ignore-scripts','--no-audit','--no-fund','--registry=https://registry.npmjs.org/','--userconfig='+str(config),'--globalconfig=/dev/null','--cache=/private/cache','--logs-dir=/private/logs','--global=false','--package-lock=true','--install-links=false','--legacy-peer-deps='+str(target.get('legacy_peer_deps',False)).lower()]
  with open(private/'output','w+b') as output:
   result=subprocess.Popen(args,cwd=root/target['directory'],env=environment,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
   remaining=1048576
   while True:
    chunk=result.stdout.read(65536)
    if not chunk: break
    if remaining:
     bounded=chunk[:remaining];output.write(bounded);remaining-=len(bounded)
   result.stdout.close();result.wait()
   if result.returncode:
    output.seek(0);text=output.read(1048576).decode('utf-8','replace')
    codes=re.findall(r'(?im)^npm (?:ERR!|error) code (E[A-Z]+)\s*$',text)
    code=codes[-1] if codes else ''
    if code in {'EUSAGE','EBADENGINE','ELOCKVERIFY'}:
     failure={'code':code,'category':'permanent','node_version':version([node,'--version']),'npm_version':version([node,npm,'--version']),'phase_durations_ms':{'install':min(600000,int((time.monotonic()-started)*1000))}}
    raise RuntimeError('npm installation failed')
  (root/target['directory']/'node_modules').mkdir(exist_ok=True)
except Exception:
 if failure: print(json.dumps(failure),flush=True)
 sys.exit(1)
finally:
 for target in private.iterdir():
  if target.is_dir(): shutil.rmtree(target)
  else: target.unlink()
`

const npmSetupExportPython = `
import os,pathlib,tarfile,sys
root=pathlib.Path('/setup/project')
with tarfile.open(fileobj=sys.stdout.buffer,mode='w|') as archive:
 for current,dirs,names in os.walk(root,followlinks=False):
  dirs[:]=sorted(d for d in dirs if not pathlib.Path(current,d).is_symlink())
  if 'node_modules' in dirs:
   folder=pathlib.Path(current,'node_modules');archive.add(folder,arcname=folder.relative_to(root).as_posix(),recursive=True);dirs.remove('node_modules')
`

const npmSetupImportPython = `
import json,os,pathlib,shutil,sys,tarfile,tempfile
p=json.loads(sys.argv[1]);root=pathlib.Path(p['work_dir'])
if root.resolve()!=root: raise RuntimeError('unsafe workspace')
def publish_outputs(stage):
 if stage.parent!=root or stage.resolve()!=stage or not stage.name.startswith('.143-npm-stage-'): raise RuntimeError('unsafe stage')
 with tarfile.open(fileobj=sys.stdin.buffer,mode='r|') as archive: archive.extractall(stage,filter='data')
 outputs=[]
 for current,dirs,names in os.walk(stage,followlinks=False):
  if 'node_modules' in dirs:
   folder=pathlib.Path(current,'node_modules');outputs.append(folder.relative_to(stage));dirs.remove('node_modules')
 for previous in p.get('previous_outputs',[]):
  relative=pathlib.PurePosixPath(previous)
  if relative.is_absolute() or '..' in relative.parts or relative.name!='node_modules': raise RuntimeError('unsafe prior output')
  destination=root/relative
  if destination.parent.resolve()!=destination.parent: raise RuntimeError('unsafe prior destination')
  if destination.is_symlink(): destination.unlink()
  elif destination.exists(): shutil.rmtree(destination)
 for relative in sorted(outputs,key=lambda entry:len(entry.parts)):
  destination=root/relative
  if destination.parent.resolve()!=destination.parent or not destination.parent.is_dir(): raise RuntimeError('unsafe destination')
  if destination.is_symlink(): destination.unlink()
  elif destination.exists(): shutil.rmtree(destination)
  shutil.move(str(stage/relative),destination)
publish_outputs(pathlib.Path(p['stage_dir']))
`

type npmSetupNetworkProfile struct {
	Network    string
	ResolvConf string
	DNS        []string
}

func npmSetupNetworkFromSandbox(inspect container.InspectResponse) (npmSetupNetworkProfile, error) {
	if inspect.ContainerJSONBase == nil || inspect.HostConfig == nil {
		return npmSetupNetworkProfile{}, errors.New("npm setup sandbox network unavailable")
	}
	network := string(inspect.HostConfig.NetworkMode)
	if network == "" || network == "host" || network == "none" || strings.HasPrefix(network, "container:") {
		return npmSetupNetworkProfile{}, errors.New("npm setup requires an isolated sandbox network")
	}
	profile := npmSetupNetworkProfile{Network: network, DNS: append([]string(nil), inspect.HostConfig.DNS...)}
	for _, entry := range inspect.HostConfig.Mounts {
		if entry.Target == "/etc/resolv.conf" {
			if entry.Type != mount.TypeBind || !entry.ReadOnly || entry.Source == "" {
				return npmSetupNetworkProfile{}, errors.New("npm setup sandbox resolver unavailable")
			}
			profile.ResolvConf = entry.Source
		}
	}
	return profile, nil
}

func (d *DockerProvider) npmSetupMainExec(ctx context.Context, sb *agent.Sandbox, script, control string, input io.Reader, output io.Writer) (int, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return -1, errors.New("npm setup operation requires a deadline")
	}
	marker := "/tmp/143-npm-control-" + uuid.NewString()
	command := "/usr/bin/python3 -I -S -c '" + shellEscape(npmSetupMainWrapperPython) + "' '" + shellEscape(script) + "' '" + shellEscape(control) + "' '" + fmt.Sprint(float64(deadline.UnixNano())/1e9) + "' '" + marker + "'"
	clean := npmSetupSandbox(sb)
	if input == nil {
		input = strings.NewReader("")
	}
	code, execErr := d.ExecWithStdinForReviewPreparation(ctx, &clean, command, input, output, io.Discard)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
	defer cancel()
	cleanupControl, marshalErr := json.Marshal(map[string]any{"marker": marker, "cancel": ctx.Err() != nil || execErr != nil, "quarantine": "/tmp/143-npm-setup-quarantined"})
	if marshalErr != nil {
		return -1, errors.New("npm setup cleanup control unavailable")
	}
	confirmation := "/usr/bin/python3 -I -S -c '" + shellEscape(npmSetupMainCleanupPython) + "' '" + shellEscape(string(cleanupControl)) + "'"
	status, cleanupErr := d.ExecForReviewPreparation(cleanupCtx, &clean, confirmation, io.Discard, io.Discard)
	if cleanupErr != nil || status != 0 {
		return -1, errors.New("npm setup process cleanup unconfirmed; workspace must be recreated")
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	return code, execErr
}

const npmSetupMainWrapperPython = `
import json,os,pathlib,shutil,signal,stat,subprocess,sys,tempfile,time
script,control,deadline,marker=sys.argv[1:];deadline=float(deadline);active=None;stage=None
quarantine='/tmp/143-npm-setup-quarantined'
def stop(signum,frame): raise SystemExit(128+signum)
signal.signal(signal.SIGTERM,stop);signal.signal(signal.SIGINT,stop)
try:
 if os.path.lexists(quarantine): raise RuntimeError('workspace quarantined')
 settings=json.loads(control)
 if settings.get('import'):
  stage=tempfile.mkdtemp(prefix='.143-npm-stage-',dir=settings['work_dir'])
  settings['stage_dir']=stage;control=json.dumps(settings)
 active=subprocess.Popen(['/usr/bin/python3','-I','-S','-c',script,control],stdin=sys.stdin.buffer,stdout=sys.stdout.buffer,stderr=subprocess.DEVNULL,close_fds=True,start_new_session=True)
 while active.poll() is None:
  if time.time()>=deadline or os.path.lexists(marker): raise TimeoutError('npm setup interrupted')
  time.sleep(.05)
 code=active.returncode
finally:
 if active is not None:
  try: os.killpg(active.pid,signal.SIGKILL)
  except ProcessLookupError: pass
  active.wait(timeout=3)
 if stage is not None: shutil.rmtree(stage)
 fd,temporary=tempfile.mkstemp(prefix='143-npm-done-',dir='/tmp')
 try:
  with os.fdopen(fd,'w') as stream: stream.write('cleaned\n')
  os.replace(temporary,marker+'.done')
 finally:
  if os.path.exists(temporary): os.unlink(temporary)
sys.exit(code)
`

const npmSetupMainCleanupPython = `
import json,os,stat,sys,time
p=json.loads(sys.argv[1]);marker=p['marker'];done=marker+'.done'
if p['cancel']:
 fd=os.open(marker,os.O_CREAT|os.O_EXCL|os.O_WRONLY|os.O_NOFOLLOW|os.O_NONBLOCK,0o600);os.close(fd)
limit=time.monotonic()+4
while not os.path.lexists(done) and time.monotonic()<limit: time.sleep(.05)
valid=False
try:
 fd=os.open(done,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
 with os.fdopen(fd,'rb') as stream:
  info=os.fstat(stream.fileno());valid=stat.S_ISREG(info.st_mode) and info.st_size==8 and stream.read(9)==b'cleaned\n'
except OSError: pass
if not valid:
 try:
  fd=os.open(p['quarantine'],os.O_CREAT|os.O_EXCL|os.O_WRONLY|os.O_NOFOLLOW|os.O_NONBLOCK,0o600);os.close(fd)
 except FileExistsError: pass
for target in (marker,done):
 try: os.unlink(target)
 except FileNotFoundError: pass
sys.exit(0 if valid else 1)
`
