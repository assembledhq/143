    expect(screen.getByText("3 files changed")).toBeInTheDocument();
  });

  it("preserves the incoming file order", () => {
    const orderedFiles: DiffFile[] = [
      makeDiffFile("src/z-last.ts", 1, 0),
      makeDiffFile("src/a-first.ts", 1, 0),
      makeDiffFile("README.md", 1, 0),
    ];

    render(
      <FileTree files={orderedFiles} activeFileIndex={0} onFileSelect={vi.fn()} />
    );

    expect(
      screen.getAllByRole("button").map((button) => button.textContent)
    ).toEqual(
      expect.arrayContaining([
        expect.stringContaining("z-last.ts"),
        expect.stringContaining("a-first.ts"),
        expect.stringContaining("README.md"),
      ])
    );

    const fileButtons = screen.getAllByRole("button").filter((button) =>
      ["z-last.ts", "a-first.ts", "README.md"].some((name) => button.textContent?.includes(name))
    );

    expect(fileButtons.map((button) => button.textContent)).toEqual([
      expect.stringContaining("z-last.ts"),
      expect.stringContaining("a-first.ts"),
      expect.stringContaining("README.md"),
    ]);
  });

  it("calls onFileSelect when a file is clicked", async () => {
    const onFileSelect = vi.fn();
    const user = userEvent.setup();
