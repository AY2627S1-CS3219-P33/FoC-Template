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
import { EmbeddedAuth } from "./embeddedAuth";
import {
  getConfig,
  logout as apiLogout,
  provision,
} from "../api/client";
import type { AuthConfig, Profile } from "../api/types";

type Status = "loading" | "signedOut" | "signedIn";

export interface AccessTokenOptions {
  audience?: string;
  scope?: string;
}

interface AuthContextValue {
  status: Status;
  profile: Profile | null;
  error: string | null;
  signInAuth0: (email: string, password: string) => Promise<void>;
  signUpAuth0: (name: string, email: string, password: string) => Promise<void>;
  resetPassword: (email: string) => Promise<void>;
  hostedLogin: (signup?: boolean) => void;
  getAccessToken: (options?: AccessTokenOptions) => Promise<string>;
  signOut: () => Promise<void>;
  setProfile: (profile: Profile) => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>("loading");
  const [profile, setProfileState] = useState<Profile | null>(null);
  const [error, setError] = useState<string | null>(null);
  const auth0Ref = useRef<EmbeddedAuth | null>(null);
  const initialization = useRef<Promise<void> | null>(null);

  // Establish a session from a bearer token: provision (idempotent) then hold
  // the returned profile.
  const establish = useCallback(async (token: string) => {
    const account = await provision(token);
    setProfileState(account);
    setStatus("signedIn");
    return account;
  }, []);

  const bootstrapAuth0 = useCallback(async (cfg: AuthConfig) => {
    const client = new EmbeddedAuth(cfg);
    auth0Ref.current = client;
    const token = await client.initialize();
    if (token) await establish(token);
    else setStatus("signedOut");
  }, [establish]);

  useEffect(() => {
    let cancelled = false;
    if (!initialization.current) {
      initialization.current = (async () => {
        const cfg = await getConfig();
        await bootstrapAuth0(cfg);
      })();
    }
    initialization.current.catch((err) => {
      if (!cancelled) {
        setError(err instanceof Error ? err.message : "Failed to initialize.");
        setStatus("signedOut");
      }
    });
    return () => {
      cancelled = true;
    };
  }, [establish, bootstrapAuth0]);

  const getAccessToken = useCallback(async (options?: AccessTokenOptions) => {
    if (!auth0Ref.current) throw new Error("Auth0 is not configured.");
    try {
      return await auth0Ref.current.getAccessToken(options);
    } catch (error) {
      if (options?.audience) throw error;
      setProfileState(null);
      setStatus("signedOut");
      setError("Your login could not be renewed. Please sign in again.");
      throw new Error("Your login could not be renewed. Please sign in again.");
    }
  }, []);

  const signInAuth0 = useCallback(async (email: string, password: string) => {
    if (!auth0Ref.current) throw new Error("Auth0 is not configured.");
    setError(null);
    await auth0Ref.current.login(email, password);
  }, []);

  const signUpAuth0 = useCallback(async (name: string, email: string, password: string) => {
    if (!auth0Ref.current) throw new Error("Auth0 is not configured.");
    setError(null);
    await auth0Ref.current.signup(name, email, password);
  }, []);

  const resetPassword = useCallback(async (email: string) => {
    if (!auth0Ref.current) throw new Error("Auth0 is not configured.");
    await auth0Ref.current.resetPassword(email);
  }, []);

  const hostedLogin = useCallback((signup = false) => {
    if (!auth0Ref.current) throw new Error("Auth0 is not configured.");
    auth0Ref.current.hostedLogin(signup);
  }, []);

  const signOut = useCallback(async () => {
    try {
      const token = await getAccessToken();
      await apiLogout(token);
    } catch {
      // Best-effort notification must not prevent logout.
    }
    setProfileState(null);
    setError(null);
    setStatus("signedOut");
    if (auth0Ref.current) auth0Ref.current.logout();
  }, [getAccessToken]);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      profile,
      error,
      signInAuth0,
      signUpAuth0,
      resetPassword,
      hostedLogin,
      getAccessToken,
      signOut,
      setProfile: setProfileState,
    }),
    [status, profile, error, signInAuth0, signUpAuth0, resetPassword, hostedLogin, getAccessToken, signOut],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
