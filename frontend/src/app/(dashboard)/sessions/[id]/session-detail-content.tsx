  ArrowUp,
  ClipboardList,
  ExternalLink,
  FileCode2,
  GitPullRequest,
  Loader2,
                {showValidationTab && (
                  <TabsTrigger value="validation">Validation</TabsTrigger>
                )}
                <TabsTrigger value="preview">Preview</TabsTrigger>
              </TabsList>
              {(() => {
                if (hasPR && prData?.data?.github_pr_url) {
