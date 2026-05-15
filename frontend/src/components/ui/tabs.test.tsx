import { describe, expect, it } from 'vitest';
import { renderWithProviders, screen } from '@/test/test-utils';
import { Tabs, TabsList, TabsTrigger } from './tabs';

describe('Tabs', () => {
  it('gives line-tab underlines explicit pseudo-element content', () => {
    renderWithProviders(
      <Tabs defaultValue="one">
        <TabsList variant="line" size="sm">
          <TabsTrigger value="one">One</TabsTrigger>
          <TabsTrigger value="two">Two</TabsTrigger>
        </TabsList>
      </Tabs>,
    );

    const activeTab = screen.getByRole('tab', { name: 'One' });
    expect(activeTab.className, 'line tab underline should render a real pseudo-element').toContain("after:content-['']");
    expect(activeTab.className, 'active line tab should reveal its underline').toContain('group-data-[variant=line]/tabs-list:data-[state=active]:after:opacity-100');
  });
});
