    expect(emailInput).toHaveClass('h-9');
  });

  it('uses the shared default modal action button sizing in the invite modal footer', async () => {
    renderWithProviders(<TeamSettingsPage />);

    await userEvent.click(await screen.findByRole('button', { name: 'Invite' }));

    const cancelButton = await screen.findByRole('button', { name: 'Cancel' });
    const sendInviteButton = screen.getByRole('button', { name: 'Send invite' });

    expect(cancelButton).toHaveAttribute('data-size', 'default');
    expect(sendInviteButton).toHaveAttribute('data-size', 'default');
    expect(cancelButton).toHaveClass('h-8');
    expect(sendInviteButton).toHaveClass('h-8');
  });

  it('renders pending invitations', async () => {
    renderWithProviders(<TeamSettingsPage />);
