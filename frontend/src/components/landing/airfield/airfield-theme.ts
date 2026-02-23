export interface AirfieldTheme {
  zoneGlow: (alpha: number) => string;
  text: string;
  textMuted: string;
  cardBg: string;
  cardBorder: string;
  pathStroke: string;
  dotActive: string;
  dotInactive: string;
}

export const AIRFIELD_DARK: AirfieldTheme = {
  zoneGlow: (alpha: number) => `rgba(0, 255, 100, ${alpha})`,
  text: "#e0f0e8",
  textMuted: "rgba(200, 230, 210, 0.6)",
  cardBg: "rgba(4, 12, 4, 0.75)",
  cardBorder: "rgba(0, 255, 100, 0.2)",
  pathStroke: "rgba(0, 255, 100, 0.6)",
  dotActive: "#00ff64",
  dotInactive: "rgba(0, 255, 100, 0.2)",
};

export const AIRFIELD_LIGHT: AirfieldTheme = {
  zoneGlow: (alpha: number) => `rgba(0, 200, 80, ${alpha})`,
  text: "#1a2a1e",
  textMuted: "rgba(30, 50, 35, 0.6)",
  cardBg: "rgba(240, 248, 242, 0.8)",
  cardBorder: "rgba(0, 180, 80, 0.3)",
  pathStroke: "rgba(0, 180, 80, 0.6)",
  dotActive: "#00aa44",
  dotInactive: "rgba(0, 180, 80, 0.2)",
};
