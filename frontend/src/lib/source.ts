import { docs } from "collections/server";
import { loader } from "fumadocs-core/source";
import { sidebarLabelForPageTreeFile } from "@/lib/docs/sidebar-labels";

export const source = loader({
  baseUrl: "/docs",
  pageTree: {
    transformers: [
      {
        file(node, filePath) {
          return {
            ...node,
            name: sidebarLabelForPageTreeFile(filePath, node.name),
          };
        },
      },
    ],
  },
  source: docs.toFumadocsSource(),
});
