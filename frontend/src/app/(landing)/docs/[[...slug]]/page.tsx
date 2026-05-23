import {
  DocsBody,
  DocsDescription,
  DocsPage,
  DocsTitle,
} from "fumadocs-ui/layouts/docs/page";
import { createRelativeLink } from "fumadocs-ui/mdx";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { getDocsMDXComponents } from "@/components/docs/docs-mdx-components";
import { source } from "@/lib/source";

interface DocsRouteParams {
  slug?: string[];
}

interface DocsPageProps {
  params: Promise<DocsRouteParams>;
}

export function generateStaticParams() {
  return source.generateParams();
}

export async function generateMetadata({ params }: DocsPageProps): Promise<Metadata> {
  const { slug = [] } = await params;
  const page = source.getPage(slug);

  if (!page) {
    return {};
  }

  return {
    title: `${page.data.title} | 143.dev docs`,
    description: page.data.description,
    alternates: {
      canonical: page.url,
    },
  };
}

export default async function PublicDocsPage({ params }: DocsPageProps) {
  const { slug = [] } = await params;
  const page = source.getPage(slug);

  if (!page) {
    notFound();
  }

  const MdxContent = page.data.body;

  return (
    <DocsPage toc={page.data.toc} tableOfContent={{ single: false }}>
      <DocsTitle>{page.data.title}</DocsTitle>
      <DocsDescription>{page.data.description}</DocsDescription>
      <DocsBody>
        <MdxContent
          components={getDocsMDXComponents({
            a: createRelativeLink(source, page),
          })}
        />
      </DocsBody>
    </DocsPage>
  );
}
