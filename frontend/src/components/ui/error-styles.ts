export const errorSurfaceClassNames = {
  container: "border border-destructive/25 bg-destructive/[0.055] text-foreground",
  iconContainer: "flex size-8 shrink-0 items-center justify-center rounded-full border border-destructive/20 bg-background/85 text-destructive shadow-sm",
  textWrap: "min-w-0 break-words [overflow-wrap:anywhere]",
  title: "min-w-0 break-words text-xs font-medium leading-5 text-foreground [overflow-wrap:anywhere]",
  description: "min-w-0 break-words text-xs leading-5 text-muted-foreground [overflow-wrap:anywhere]",
  action: "border-destructive/20 bg-background/85 text-foreground hover:border-destructive/30 hover:bg-background",
} as const;
