export interface AirfieldTheme {
  terrain: string;
  tarmac: string;
  runway: string;
  markings: string;
  buildingFill: string;
  buildingStroke: string;
  zoneGlow: (alpha: number) => string;
  runwayLight: string;
  runwayLightDim: string;
  text: string;
  textMuted: string;
  cardBg: string;
  cardBorder: string;
  pathStroke: string;
  dotActive: string;
  dotInactive: string;
}

export const AIRFIELD_DARK: AirfieldTheme = {
  terrain: "#0a0e14",
  tarmac: "#141a24",
  runway: "#1a2030",
  markings: "#3a4560",
  buildingFill: "#1c2436",
  buildingStroke: "#2a3a54",
  zoneGlow: (alpha: number) => `rgba(255, 180, 50, ${alpha})`,
  runwayLight: "#ffb830",
  runwayLightDim: "#4a3a20",
  text: "#e8ecf2",
  textMuted: "rgba(232, 236, 242, 0.5)",
  cardBg: "rgba(10, 14, 20, 0.7)",
  cardBorder: "rgba(255, 180, 50, 0.2)",
  pathStroke: "rgba(255, 180, 50, 0.6)",
  dotActive: "#ffb830",
  dotInactive: "#3a4560",
};

export const AIRFIELD_LIGHT: AirfieldTheme = {
  terrain: "#c4b99a",
  tarmac: "#9a9284",
  runway: "#7a7468",
  markings: "#e8e0cc",
  buildingFill: "#8a8272",
  buildingStroke: "#6a6458",
  zoneGlow: (alpha: number) => `rgba(60, 130, 220, ${alpha})`,
  runwayLight: "#7aa4d4",
  runwayLightDim: "#9a9284",
  text: "#2a2520",
  textMuted: "rgba(42, 37, 32, 0.6)",
  cardBg: "rgba(220, 215, 200, 0.75)",
  cardBorder: "rgba(60, 130, 220, 0.3)",
  pathStroke: "rgba(60, 130, 220, 0.6)",
  dotActive: "#3c82dc",
  dotInactive: "#9a9284",
};
