// Session detail geometry.
//
// Most of these are read by both the loaded workspace and the loading skeleton
// that stands in for it during a session transition: the skeleton reserves the
// same boxes as the real chrome so swapping between them never shifts layout,
// which only holds if both sides read the same values. The panel width bounds
// below are session detail geometry too, but only the resize handler consumes
// them — the skeleton has no counterpart to keep in step.

export const SESSION_HEADER_HEIGHT_CLASSNAME = "h-14";

export const SESSION_WORKSPACE_MIN_WIDTH_CLASSNAME = "md:min-w-[440px]";

// AgentTabStrip: px-3 py-2 wrapper (8px + 8px) around a min-h-9 row (36px)
// plus its 1px bottom border.
export const SESSION_THREAD_STRIP_HEIGHT_CLASSNAME = "h-[53px]";

// Composer input surface: the textarea's min-h-[44px] above the h-8 action
// row in its px-2 pb-2 wrapper (32px + 8px).
export const SESSION_COMPOSER_SURFACE_HEIGHT_CLASSNAME = "h-[84px]";

export const SESSION_DETAIL_PANEL_DEFAULT_WIDTH = 384;
export const SESSION_DETAIL_PANEL_MIN_WIDTH = 280;
export const SESSION_DETAIL_PANEL_MAX_WIDTH = 600;
