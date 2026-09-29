import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createAuth0Client, type Auth0Client } from "@auth0/auth0-spa-js";
import {
  getConfig,
  getProfile,
  logout as apiLogout,
  mintDevToken,
  provision,
} from "../api/client";
import type { AuthConfig, Profile } from "../api/types";

type Status = "loading" | "signedOut" | "signedIn";

interface AuthContextValue {
  status: Status;
  config: AuthConfig | null;
  profile: Profile | null;
  error: string | null;
  signInDev: (identifier: string, remember: boolean) => Promise<void>;
  signUpDev: (username: string, email: string, remember: boolean) => Promise<void>;
  signInAuth0: () => Promise<void>;
  signOut: () => Promise<void>;
  setProfile: (profile: Profile) => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

const TOKEN_KEY = "foc.user.token";

// Persist the dev token across reloads. "Remember me" chooses durable storage;
// otherwise the token lives only for the browser session.
function persistToken(token: string, remember: boolean): void {
  try {
    (remember ? localStorage : sessionStorage).setItem(TOKEN_KEY, token);
    (remember ? sessionStorage : localStorage).removeItem(TOKEN_KEY);
  } catch {
    // Storage may be unavailable (private mode); in-memory state still works.
  }
}

function readToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? sessionStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

function clearToken(): void {
  try {
    localStorage.removeItem(TOKEN_KEY);
    sessionStorage.removeItem(TOKEN_KEY);
  } catch {
    // ignore
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>("loading");
  const [config, setConfig] = useState<AuthConfig | null>(null);
  const [profile, setProfileState] = useState<Profile | null>(null);
  const [error, setError] = useState<string | null>(null);
  const auth0Ref = useRef<Auth0Client | null>(null);

  // Establish a session from a bearer token: provision (idempotent) then hold
  // the returned profile. Any failure clears the persisted token.
  const establish = useCallback(async (token: string) => {
    const account = await provision(token);
    setProfileState(account);
    setStatus("signedIn");
    return account;
  }, []);

  const bootstrapAuth0 = useCallback(async (cfg: AuthConfig) => {
    const client = await createAuth0Client({
      domain: cfg.domain,
      clientId: cfg.clientId,
      cacheLocation: "localstorage",
      authorizationParams: {
        redirect_uri: window.location.origin,
        audience: cfg.audience,
        scope: "openid profile email",
      },
    });
    auth0Ref.current = client;

    const query = new URLSearchParams(window.location.search);
    if ((query.has("code") || query.has("error")) && query.has("state")) {
      try {
        await client.handleRedirectCallback();
      } finally {
        window.history.replaceState({}, document.title, window.location.pathname);
      }
    }
    if (await client.isAuthenticated()) {
      const token = await client.getTokenSilently();
      if (token) {
        await establish(token);
        return;
      }
    }
    setStatus("signedOut");
  }, [establish]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const cfg = await getConfig();
        if (cancelled) return;
        setConfig(cfg);

        if (cfg.dev) {
          const existing = readToken();
          if (existing) {
            try {
              // Validate the persisted token before trusting it.
              await getProfile(existing);
              await establish(existing);
              return;
            } catch {
              clearToken();
            }
          }
          setStatus("signedOut");
          return;
        }

        await bootstrapAuth0(cfg);
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Failed to initialize.");
          setStatus("signedOut");
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [establish, bootstrapAuth0]);

  // DEV-ONLY: mint a token for an existing identity. A bare username is treated
  // as an NUS student address so provisioning succeeds.
  const signInDev = useCallback(
    async (identifier: string, remember: boolean) => {
      setError(null);
      const trimmed = identifier.trim();
      const email = trimmed.includes("@") ? trimmed : `${trimmed}@u.nus.edu`;
      const local = email.split("@")[0] || "devuser";
      await mintProvisionPersist(
        { sub: `auth0|${local}`, email, nickname: local, name: local },
        remember,
      );
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [establish],
  );

  // DEV-ONLY: stand in for Auth0 sign-up. The chosen username becomes the local
  // username, mirroring how Auth0's nickname maps in production.
  const signUpDev = useCallback(
    async (username: string, email: string, remember: boolean) => {
      setError(null);
      const nickname = username.trim() || email.split("@")[0];
      await mintProvisionPersist(
        { sub: `auth0|${nickname}`, email: email.trim(), nickname, name: nickname },
        remember,
      );
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [establish],
  );

  async function mintProvisionPersist(
    identity: { sub: string; email: string; nickname: string; name: string },
    remember: boolean,
  ) {
    const minted = await mintDevToken(identity);
    persistToken(minted.access_token, remember);
    try {
      await establish(minted.access_token);
    } catch (err) {
      clearToken();
      throw err;
    }
  }

  const signInAuth0 = useCallback(async () => {
    if (!auth0Ref.current) throw new Error("Auth0 is not configured.");
    await auth0Ref.current.loginWithRedirect();
  }, []);

  const signOut = useCallback(async () => {
    const token = readToken();
    if (token) {
      try {
        await apiLogout(token);
      } catch {
        // Best-effort notification to the service.
      }
    }
    clearToken();
    setProfileState(null);
    setStatus("signedOut");
    if (auth0Ref.current && !config?.dev) {
      await auth0Ref.current.logout({
        logoutParams: { returnTo: window.location.origin },
      });
    }
  }, [config]);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      config,
      profile,
      error,
      signInDev,
      signUpDev,
      signInAuth0,
      signOut,
      setProfile: setProfileState,
    }),
    [status, config, profile, error, signInDev, signUpDev, signInAuth0, signOut],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
