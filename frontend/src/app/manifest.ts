import type { MetadataRoute } from "next";

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "143",
    short_name: "143",
    description: "Shared coding-agent infrastructure for engineering teams.",
    start_url: "/",
    scope: "/",
    display: "standalone",
    theme_color: "#091f33",
    background_color: "#091f33",
    icons: [
      {
        src: "/icon.svg",
        sizes: "any",
        type: "image/svg+xml",
        purpose: "any",
      },
      {
        src: "/icon.svg",
        sizes: "any",
        type: "image/svg+xml",
        purpose: "maskable",
      },
    ],
  };
}
