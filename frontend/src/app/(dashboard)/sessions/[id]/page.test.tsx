
    const previewTab = screen.getByRole('tab', { name: /Preview/ });
    expect(previewTab).toBeInTheDocument();
    expect(previewTab.querySelector('svg')).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(previewTab);
