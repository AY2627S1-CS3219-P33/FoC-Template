import { useCallback, useEffect, useRef, useState } from "react";

interface Page<T> {
  items: T[];
  nextCursor?: string;
}

interface Result<T> {
  items: T[];
  loading: boolean;
  error: string | null;
  hasMore: boolean;
  loadMore: () => void;
  reload: () => void;
}

// Cursor paging shared by every errand list. A cursor is bound to the filters
// that produced it, so changing a filter must start a fresh sequence: callers
// pass a new `load` (via useCallback on the filters) and the effect reloads
// from the first page.
//
// Responses are applied only if they belong to the newest request, so a slow
// page cannot overwrite a newer one.
export function usePagedList<T>(
  load: (cursor?: string) => Promise<Page<T>>,
  messageFor: (err: unknown) => string,
): Result<T> {
  const [items, setItems] = useState<T[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestId = useRef(0);

  const fetchPage = useCallback(
    async (cursor?: string) => {
      const id = ++requestId.current;
      setLoading(true);
      setError(null);
      try {
        const page = await load(cursor);
        if (id !== requestId.current) return;
        setItems((current) => (cursor ? [...current, ...page.items] : page.items));
        setNextCursor(page.nextCursor);
      } catch (err) {
        if (id !== requestId.current) return;
        setError(messageFor(err));
        if (!cursor) {
          setItems([]);
          setNextCursor(undefined);
        }
      } finally {
        if (id === requestId.current) setLoading(false);
      }
    },
    [load, messageFor],
  );

  useEffect(() => {
    void fetchPage();
    // Abandon in-flight responses when the filters change or the list unmounts.
    return () => {
      requestId.current += 1;
    };
  }, [fetchPage]);

  return {
    items,
    loading,
    error,
    hasMore: Boolean(nextCursor),
    loadMore: () => void fetchPage(nextCursor),
    reload: () => void fetchPage(),
  };
}
