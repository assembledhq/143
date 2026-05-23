interface RawDocsPage {
  slugs: string[];
}

export function getRawDocsStaticParams(pages: RawDocsPage[]) {
  return pages
    .filter((page) => page.slugs.length > 0)
    .map((page) => ({ slug: page.slugs }));
}
