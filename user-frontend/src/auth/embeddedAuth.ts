import { WebAuth, type Auth0DecodedHash, type Auth0Error } from "auth0-js";
import type { AuthConfig } from "../api/types";

export function authError(error: Auth0Error): Error {
  const code = error.code || error.error;
  const messages: Record<string, string> = {
    access_denied: "Incorrect email or password, or sign-in is not permitted.",
    invalid_user_password: "Incorrect email or password. Please try again.",
    user_exists: "Could not create this account. Try signing in or resetting your password.",
    username_exists: "Could not create this account. Try signing in or resetting your password.",
    PasswordStrengthError: "Auth0 rejected the password. Check the password requirements.",
    invalid_password: "Auth0 rejected the password. Check the password requirements.",
    password_leaked: "This password has appeared in a data breach. Choose another password.",
    too_many_attempts: "Too many attempts. Wait before trying again.",
    too_many_requests: "Too many requests. Wait before trying again.",
    unauthorized_client: "Embedded login is not enabled for this application or origin.",
    requires_verification: "Auth0 requires an additional verification challenge. Continue on the Auth0 page.",
    blocked_user: "Sign-in is blocked for this account.",
    request_error: "Could not reach Auth0. Check your connection and try again.",
  };
  // Do not log or expose the raw response, which may contain submitted values.
  return new Error(messages[code] || "Auth0 could not complete this request. Try the Auth0 page or contact support.");
}

export class EmbeddedAuth {
  private readonly webAuth: WebAuth;
  private readonly connection: string;
  private accessToken = "";
  private expiresAt = 0;
  private renewal: Promise<string> | null = null;

  constructor(config: AuthConfig) {
    this.connection = import.meta.env.VITE_AUTH0_CONNECTION || "Username-Password-Authentication";
    this.webAuth = new WebAuth({
      domain: config.domain,
      clientID: config.clientId,
      audience: config.audience,
      redirectUri: window.location.origin + "/",
      responseType: "token id_token",
      scope: "openid profile email",
    });
  }

  private accept(result: Auth0DecodedHash | null | undefined): string {
    const seconds = Number(result?.expiresIn);
    if (!result?.accessToken || !Number.isFinite(seconds) || seconds <= 0) {
      throw new Error("Auth0 did not return a valid API access token.");
    }
    this.accessToken = result.accessToken;
    this.expiresAt = Date.now() + seconds * 1000;
    return this.accessToken;
  }

  async initialize(): Promise<string | null> {
    const hash = new URLSearchParams(window.location.hash.slice(1));
    if (hash.has("access_token") || hash.has("id_token") || hash.has("error")) {
      // Auth0.js validates the transaction state, nonce, and ID-token signature.
      try {
        const result = await new Promise<Auth0DecodedHash | null>((resolve, reject) => {
          this.webAuth.parseHash({}, (error, value) => {
            if (error) reject(authError(error));
            else resolve(value);
          });
        });
        return this.accept(result);
      } finally {
        window.history.replaceState({}, document.title, window.location.pathname + window.location.search);
      }
    }
    try {
      return await this.getAccessToken();
    } catch (error) {
      const code = (error as { code?: string }).code;
      if (["login_required", "consent_required", "interaction_required", "timeout"].includes(code || "")) {
        return null;
      }
      throw error;
    }
  }

  getAccessToken(): Promise<string> {
    if (this.accessToken && Date.now() + 30_000 < this.expiresAt) {
      return Promise.resolve(this.accessToken);
    }
    if (!this.renewal) {
      this.renewal = new Promise<string>((resolve, reject) => {
        this.webAuth.checkSession({}, (error, result) => {
          if (error) {
            this.accessToken = "";
            this.expiresAt = 0;
            reject(Object.assign(authError(error), { code: error.code || error.error }));
            return;
          }
          try { resolve(this.accept(result)); } catch (err) { reject(err); }
        });
      }).finally(() => { this.renewal = null; });
    }
    return this.renewal;
  }

  login(email: string, password: string): Promise<void> {
    return new Promise((_, reject) => {
      this.webAuth.login({ realm: this.connection, email: email.trim(), password }, (error) => {
        // Successful authentication navigates to the callback URL.
        reject(error ? authError(error) : new Error("Sign-in did not redirect. Please try again."));
      });
    });
  }

  signup(name: string, email: string, password: string): Promise<void> {
    // Preserve the current nickname-based provisioning seam. This is not an
    // Auth0 username identifier and does not guarantee name uniqueness.
    const options = {
      connection: this.connection,
      email: email.trim(),
      password,
      nickname: name.trim(),
      name: name.trim(),
    };
    return new Promise((resolve, reject) => {
      this.webAuth.signup(options, (error) => {
        if (error) reject(authError(error));
        else resolve();
      });
    });
  }

  resetPassword(email: string): Promise<void> {
    return new Promise((resolve, reject) => {
      this.webAuth.changePassword({ connection: this.connection, email: email.trim() }, (error) => {
        if (error) reject(authError(error));
        else resolve();
      });
    });
  }

  hostedLogin(signup = false): void {
    this.webAuth.authorize(signup ? { screen_hint: "signup" } : {});
  }

  logout(): void {
    this.accessToken = "";
    this.expiresAt = 0;
    this.webAuth.logout({ returnTo: window.location.origin + "/" });
  }
}
