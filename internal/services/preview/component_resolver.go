package preview

// ComponentResolverScript is the JavaScript that gets injected into every preview
// HTML page by the preview gateway. It registers __143_resolveElement(element) which
// returns component metadata for the given DOM element.
//
// The returned object has the shape:
//
//	{
//	  componentName:  string | null,
//	  componentFile:  string | null,
//	  componentLine:  number | null,
//	  props:          Record<string, any> | null,
//	  componentTree:  string[] | null,     // ancestor component names, root-first
//	  framework:      "react" | "vue" | "svelte" | "angular" | null,
//	}
//
// When no framework is detected or the element has no component owner, all
// component-specific fields are null.
const ComponentResolverScript = `
(function() {
  "use strict";

  // Guard against double-injection.
  if (window.__143_resolveElement) return;

  // =========================================================================
  // Framework detection
  // =========================================================================

  function detectFramework() {
    if (window.__REACT_DEVTOOLS_GLOBAL_HOOK__ || document.querySelector("[data-reactroot]")) {
      return "react";
    }
    if (window.__VUE_DEVTOOLS_GLOBAL_HOOK__ || document.querySelector("[data-v-app]")) {
      return "vue";
    }
    // Svelte annotates compiled elements with __svelte_meta in dev mode.
    if (document.querySelector("[class]") && findSvelteRoot()) {
      return "svelte";
    }
    if (window.ng && typeof window.ng.getComponent === "function") {
      return "angular";
    }
    return null;
  }

  function findSvelteRoot() {
    // Walk a small sample of elements looking for __svelte_meta.
    var els = document.querySelectorAll("*");
    var limit = Math.min(els.length, 200);
    for (var i = 0; i < limit; i++) {
      if (els[i].__svelte_meta) return true;
    }
    return false;
  }

  // =========================================================================
  // React resolver
  // =========================================================================

  function getReactFiberKey(el) {
    var keys = Object.keys(el);
    for (var i = 0; i < keys.length; i++) {
      if (keys[i].startsWith("__reactFiber$") || keys[i].startsWith("__reactInternalInstance$")) {
        return keys[i];
      }
    }
    return null;
  }

  function resolveReact(el) {
    var fiberKey = getReactFiberKey(el);
    if (!fiberKey) return null;

    var fiber = el[fiberKey];
    if (!fiber) return null;

    // Walk up to find the nearest function/class component (tag 0 = FunctionComponent,
    // tag 1 = ClassComponent, tag 11 = ForwardRef, tag 15 = SimpleMemoComponent).
    var componentTags = {0:1, 1:1, 11:1, 15:1};
    var current = fiber;
    var componentFiber = null;

    while (current) {
      if (componentTags[current.tag] && typeof current.type === "function") {
        componentFiber = current;
        break;
      }
      // ForwardRef wraps in an object with a render function.
      if (current.tag === 11 && current.type && current.type.render) {
        componentFiber = current;
        break;
      }
      current = current.return;
    }

    if (!componentFiber) return null;

    var name = componentFiber.type.displayName || componentFiber.type.name || "Anonymous";
    var source = componentFiber._debugSource || null;
    var props = null;
    try {
      props = serializeProps(componentFiber.memoizedProps);
    } catch(e) { /* ignore serialization errors */ }

    var tree = buildReactTree(componentFiber);

    return {
      componentName: name,
      componentFile: source ? source.fileName : null,
      componentLine: source ? source.lineNumber : null,
      props: props,
      componentTree: tree,
      framework: "react"
    };
  }

  function buildReactTree(fiber) {
    var tree = [];
    var current = fiber;
    var limit = 50;
    while (current && limit-- > 0) {
      var componentTags = {0:1, 1:1, 11:1, 15:1};
      if (componentTags[current.tag] && current.type) {
        var n = null;
        if (typeof current.type === "function") {
          n = current.type.displayName || current.type.name;
        } else if (current.type && current.type.render) {
          n = current.type.displayName || current.type.render.displayName || current.type.render.name;
        }
        if (n) tree.push(n);
      }
      current = current.return;
    }
    // tree is leaf-first, reverse to root-first.
    tree.reverse();
    return tree;
  }

  // =========================================================================
  // Vue 3 resolver
  // =========================================================================

  function getVueInstance(el) {
    // Vue 3 attaches __vueParentComponent on the root and __vnode on children.
    if (el.__vueParentComponent) return el.__vueParentComponent;

    // Walk up to find the nearest Vue component instance.
    var current = el;
    while (current) {
      if (current.__vueParentComponent) return current.__vueParentComponent;
      // Also check for Vue 3 internal keys.
      var keys = Object.keys(current);
      for (var i = 0; i < keys.length; i++) {
        if (keys[i].startsWith("__vue")) {
          var val = current[keys[i]];
          if (val && val.$ && val.$.type) return val.$;
        }
      }
      current = current.parentElement;
    }
    return null;
  }

  function resolveVue(el) {
    var instance = getVueInstance(el);
    if (!instance) return null;

    var component = instance.type || instance.$options || {};
    var name = component.name || component.__name || "Anonymous";

    var file = component.__file || null;
    var props = null;
    try {
      if (instance.props) {
        props = serializeProps(instance.props);
      } else if (instance.$props) {
        props = serializeProps(instance.$props);
      }
    } catch(e) { /* ignore */ }

    // Build component tree by walking parent chain.
    var tree = [];
    var cur = instance;
    var limit = 50;
    while (cur && limit-- > 0) {
      var n = (cur.type || cur.$options || {}).name || (cur.type || cur.$options || {}).__name;
      if (n) tree.push(n);
      cur = cur.parent || cur.$parent || null;
    }
    tree.reverse();

    return {
      componentName: name,
      componentFile: file,
      componentLine: null,
      props: props,
      componentTree: tree.length > 0 ? tree : null,
      framework: "vue"
    };
  }

  // =========================================================================
  // Svelte resolver
  // =========================================================================

  function resolveSvelte(el) {
    // Walk up from the element looking for __svelte_meta.
    var current = el;
    var limit = 50;
    while (current && limit-- > 0) {
      if (current.__svelte_meta) {
        var meta = current.__svelte_meta;
        var loc = meta.loc || {};
        return {
          componentName: extractFilenameComponent(loc.file),
          componentFile: loc.file || null,
          componentLine: loc.line || null,
          props: null,
          componentTree: null,
          framework: "svelte"
        };
      }
      current = current.parentElement;
    }

    // Svelte 5 with runes: check for $$ property on component context.
    current = el;
    limit = 50;
    while (current && limit-- > 0) {
      var keys = Object.keys(current);
      for (var i = 0; i < keys.length; i++) {
        if (keys[i].startsWith("$$") && current[keys[i]] && current[keys[i]].ctx) {
          return {
            componentName: null,
            componentFile: null,
            componentLine: null,
            props: null,
            componentTree: null,
            framework: "svelte"
          };
        }
      }
      current = current.parentElement;
    }

    return null;
  }

  function extractFilenameComponent(filePath) {
    if (!filePath) return null;
    var parts = filePath.replace(/\\/g, "/").split("/");
    var file = parts[parts.length - 1];
    // Remove .svelte extension.
    return file.replace(/\.svelte$/, "") || file;
  }

  // =========================================================================
  // Angular resolver
  // =========================================================================

  function resolveAngular(el) {
    if (!window.ng || typeof window.ng.getComponent !== "function") return null;

    // Walk up to find the nearest Angular component host element.
    var current = el;
    var limit = 50;
    while (current && limit-- > 0) {
      try {
        var comp = window.ng.getComponent(current);
        if (comp) {
          var ctor = comp.constructor;
          var name = ctor ? ctor.name : "Unknown";

          var props = null;
          try {
            // Extract @Input() values from the component instance.
            var inputProps = {};
            var inputKeys = Object.keys(comp).filter(function(k) {
              return !k.startsWith("_") && typeof comp[k] !== "function";
            });
            inputKeys.forEach(function(k) {
              inputProps[k] = safeValue(comp[k]);
            });
            if (Object.keys(inputProps).length > 0) {
              props = inputProps;
            }
          } catch(e) { /* ignore */ }

          // Build tree by walking up host elements.
          var tree = [];
          var hostEl = current;
          var treeLimit = 50;
          while (hostEl && treeLimit-- > 0) {
            try {
              var parentComp = window.ng.getComponent(hostEl);
              if (parentComp && parentComp.constructor) {
                tree.push(parentComp.constructor.name);
              }
            } catch(e) { break; }
            hostEl = hostEl.parentElement;
          }
          tree.reverse();

          return {
            componentName: name,
            componentFile: null,
            componentLine: null,
            props: props,
            componentTree: tree.length > 0 ? tree : null,
            framework: "angular"
          };
        }
      } catch(e) { /* ng.getComponent can throw for non-component elements */ }
      current = current.parentElement;
    }

    return null;
  }

  // =========================================================================
  // Serialization helpers
  // =========================================================================

  function safeValue(val, depth) {
    if (depth === undefined) depth = 0;
    if (depth > 5) return "[MaxDepth]";
    if (val === null || val === undefined) return val;
    var t = typeof val;
    if (t === "string" || t === "number" || t === "boolean") return val;
    if (t === "function") return "[Function]";
    if (val instanceof HTMLElement) return "[HTMLElement: " + val.tagName.toLowerCase() + "]";
    if (Array.isArray(val)) {
      if (val.length > 20) return "[Array(" + val.length + ")]";
      return val.map(function(v) { return safeValue(v, depth + 1); });
    }
    if (t === "object") {
      // Avoid circular references and React elements.
      if (val.$$typeof) return "[ReactElement]";
      var keys = Object.keys(val);
      if (keys.length > 30) return "[Object(" + keys.length + " keys)]";
      var out = {};
      keys.forEach(function(k) {
        out[k] = safeValue(val[k], depth + 1);
      });
      return out;
    }
    return String(val);
  }

  function serializeProps(propsObj) {
    if (!propsObj || typeof propsObj !== "object") return null;
    var keys = Object.keys(propsObj);
    if (keys.length === 0) return null;
    var out = {};
    var hasAny = false;
    keys.forEach(function(k) {
      // Skip React internal props that are not useful.
      if (k === "children" || k === "key" || k === "ref") return;
      out[k] = safeValue(propsObj[k]);
      hasAny = true;
    });
    return hasAny ? out : null;
  }

  // =========================================================================
  // Main resolver
  // =========================================================================

  window.__143_resolveElement = function(element) {
    if (!(element instanceof HTMLElement)) return null;

    var fw = detectFramework();
    var result = null;

    if (fw === "react") {
      result = resolveReact(element);
    } else if (fw === "vue") {
      result = resolveVue(element);
    } else if (fw === "svelte") {
      result = resolveSvelte(element);
    } else if (fw === "angular") {
      result = resolveAngular(element);
    }

    // If we got a result from a framework resolver, return it.
    if (result) return result;

    // Fallback: try each framework resolver in case detection missed
    // (e.g., lazy-loaded frameworks that initialize after DOMContentLoaded).
    var resolvers = [resolveReact, resolveVue, resolveSvelte, resolveAngular];
    for (var i = 0; i < resolvers.length; i++) {
      result = resolvers[i](element);
      if (result) return result;
    }

    // No framework component found for this element.
    return null;
  };
})();
`
