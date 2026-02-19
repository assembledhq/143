"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";

export function useAuth() {
  const queryClient = useQueryClient();

  const { data, isLoading, isError } = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => api.auth.me(),
    retry: false,
    staleTime: 5 * 60 * 1000,
  });

  const logout = async () => {
    await api.auth.logout();
    queryClient.clear();
    window.location.href = "/login";
  };

  return {
    user: data?.data ?? null,
    isLoading,
    isAuthenticated: !!data?.data && !isError,
    logout,
  };
}

export function useAuthProviders() {
  const { data, isLoading } = useQuery({
    queryKey: ["auth", "providers"],
    queryFn: () => api.auth.providers(),
    staleTime: 60 * 60 * 1000,
  });

  return {
    providers: data?.data ?? null,
    isLoading,
  };
}
