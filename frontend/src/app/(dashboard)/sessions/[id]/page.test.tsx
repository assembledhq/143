    expect(elements.length).toBeGreaterThanOrEqual(1);
  });

  it('uses text-sm for the session header title', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);

    const headerTitle = await screen.findByRole('heading', {
      level: 1,
      name: 'Fixed TypeError by adding null check',
    });

    expect(headerTitle.className).toContain('text-sm');
  });

  it('shows agent type label', async () => {
    renderWithProviders(<SessionDetailContent id="session-abcdef12-3456-7890" />);
    await screen.findAllByText('Fixed TypeError by adding null check');
