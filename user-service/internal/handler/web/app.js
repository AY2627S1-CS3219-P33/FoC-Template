"use strict";

let authClient;

const elements = {
  status: document.querySelector("#status"),
  login: document.querySelector("#login"),
  logout: document.querySelector("#logout"),
  callAPI: document.querySelector("#call-api"),
  profile: document.querySelector("#profile"),
  avatar: document.querySelector("#avatar"),
  name: document.querySelector("#name"),
  email: document.querySelector("#email"),
  account: document.querySelector("#account"),
  accountOutput: document.querySelector("#account-output"),
  apiResult: document.querySelector("#api-result"),
  apiOutput: document.querySelector("#api-output"),
};

function setStatus(message, kind = "info") {
  elements.status.textContent = message;
  elements.status.dataset.kind = kind;
}

async function jsonResponse(response) {
  const body = await response.json().catch(() => ({ message: "The server returned an unreadable response." }));
  if (!response.ok) {
    throw new Error(body.message || `Request failed with status ${response.status}.`);
  }
  return body;
}

async function accessToken() {
  return authClient.getTokenSilently();
}

async function provisionAccount() {
  const token = await accessToken();
  const response = await fetch("/api/auth/provision", {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  });
  const account = await jsonResponse(response);
  elements.accountOutput.textContent = JSON.stringify(account, null, 2);
  elements.account.hidden = false;
  return response.status === 201;
}

async function callProtectedAPI() {
  elements.callAPI.disabled = true;
  setStatus("Calling the protected endpoint…");
  try {
    const token = await accessToken();
    const response = await fetch("/api/private", {
      headers: { Authorization: `Bearer ${token}` },
    });
    const payload = await jsonResponse(response);
    elements.apiOutput.textContent = JSON.stringify(payload, null, 2);
    elements.apiResult.hidden = false;
    setStatus("Protected request succeeded.", "success");
  } catch (error) {
    setStatus(error.message, "error");
  } finally {
    elements.callAPI.disabled = false;
  }
}

async function logout() {
  elements.logout.disabled = true;
  try {
    const token = await accessToken();
    await fetch("/api/auth/logout", {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
    });
  } catch {
    // Local notification is best-effort; Auth0 logout must still run.
  }
  authClient.logout({ logoutParams: { returnTo: window.location.origin } });
}

async function initialize() {
  try {
    const config = await jsonResponse(await fetch("/api/auth/config", { cache: "no-store" }));
    authClient = await auth0.createAuth0Client({
      domain: config.domain,
      clientId: config.clientId,
      cacheLocation: "memory",
      authorizationParams: {
        redirect_uri: window.location.origin,
        audience: config.audience,
        scope: "openid profile email",
      },
    });

    const query = new URLSearchParams(window.location.search);
    if ((query.has("code") || query.has("error")) && query.has("state")) {
      try {
        await authClient.handleRedirectCallback();
      } finally {
        window.history.replaceState({}, document.title, window.location.pathname);
      }
    }

    if (!(await authClient.isAuthenticated())) {
      elements.login.hidden = false;
      setStatus("You are signed out.");
      return;
    }

    const user = await authClient.getUser();
    elements.name.textContent = user.name || user.nickname || "Signed in";
    elements.email.textContent = user.email || "No email returned";
    if (user.picture) {
      elements.avatar.src = user.picture;
      elements.avatar.alt = `${elements.name.textContent} profile picture`;
      elements.avatar.hidden = false;
    }
    elements.profile.hidden = false;
    elements.logout.hidden = false;
    elements.callAPI.hidden = false;

    setStatus("Provisioning your local account…");
    const created = await provisionAccount();
    setStatus(created ? "Signed in and local account created." : "Signed in and local account found.", "success");
  } catch (error) {
    elements.login.hidden = !authClient;
    setStatus(error.message, "error");
  }
}

elements.login.addEventListener("click", () => {
  if (authClient) authClient.loginWithRedirect();
});
elements.logout.addEventListener("click", () => {
  if (authClient) logout();
});
elements.callAPI.addEventListener("click", callProtectedAPI);

initialize();
