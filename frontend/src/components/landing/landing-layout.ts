export const landingLayout = {
  pageRoot: "relative overflow-x-hidden",
  sectionPadding: "relative overflow-hidden px-6 py-32 sm:px-10 sm:py-40",
  pageShell: "relative mx-auto w-full max-w-[88rem]",
  sectionHeaderGrid:
    "grid gap-8 lg:grid-cols-[0.24fr_minmax(0,0.76fr)] lg:items-end",
  featureRow:
    "grid items-center gap-12 md:grid-cols-[minmax(0,0.35fr)_minmax(0,0.65fr)] md:gap-16 lg:gap-20",
  featureRowReverse:
    "grid items-center gap-12 md:grid-cols-[minmax(0,0.65fr)_minmax(0,0.35fr)] md:gap-16 lg:gap-20",
  copyColumn: "space-y-4",
  copyBody: "max-w-[30rem]",
  visualColumn: "relative w-full",
  visualFrame: "relative overflow-hidden rounded-lg shadow-2xl",
} as const;
