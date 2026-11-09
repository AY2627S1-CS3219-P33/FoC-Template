import { useCallback } from "react";
import { useAuth } from "../../auth/AuthContext";

const SUPPLIER_AUDIENCE =
  import.meta.env.VITE_SUPPLIER_AUDIENCE || "https://api.foc.local/supplier-service";

// Returns a token getter for the supplier API. Pass true to also request
// suppliers:manage, which only administrators are granted.
export function useSupplierToken(): (manage?: boolean) => Promise<string> {
  const { getAccessToken } = useAuth();
  return useCallback(
    (manage = false) =>
      getAccessToken({
        audience: SUPPLIER_AUDIENCE,
        scope: manage
          ? "openid profile email suppliers:read suppliers:manage"
          : "openid profile email suppliers:read",
      }),
    [getAccessToken],
  );
}
