import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";

// Shared cache invalidations for connect/disconnect: the repo picker, the
// integrations page (renders chips + counts), and the repo context switcher
// summary all reflect status changes.
function invalidateRepositoryQueries(queryClient: ReturnType<typeof useQueryClient>) {
  queryClient.invalidateQueries({ queryKey: ["repositories"] });
  queryClient.invalidateQueries({ queryKey: ["integrations"] });
  queryClient.invalidateQueries({ queryKey: ["repositories", "summary"] });
}

export function useDisconnectRepository() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.repositories.disconnect(id),
    onSuccess: () => invalidateRepositoryQueries(queryClient),
  });
}

export function useReconnectRepository() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.repositories.reconnect(id),
    onSuccess: () => invalidateRepositoryQueries(queryClient),
  });
}
