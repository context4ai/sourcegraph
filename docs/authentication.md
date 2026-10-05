# Authentication

Google OIDC and GitHub OAuth identify users by provider and stable subject ID. Matching email addresses do not merge identities or grant administrator access. The first verified identity atomically becomes the administrator. Configure one provider and complete the owner's first sign-in before enabling other providers or promoting the site.

The site setting **Allow other users to sign in or register** defaults to off. Administrators may still sign in. Rejected users receive no profile or session; existing non-administrator sessions and personal API keys also lose access. Anonymous reading of explicitly public repositories remains available. Administrators can edit both language notices; the refusal page returns to a validated local reading page after five seconds.

## Demo callback addresses

The initial service origin is `https://context4ai-sourcegraph.fly.dev`.

- Google: `https://context4ai-sourcegraph.fly.dev/sourcegraph/auth/google/callback`
- GitHub: `https://context4ai-sourcegraph.fly.dev/sourcegraph/auth/github/callback`

Use the same origin in `SOURCEGRAPH_ORIGIN`. When the portal becomes the public entry point, update this setting and the provider callbacks together. For the planned custom domain, use `https://context4ai.org/sourcegraph/auth/google/callback` and `https://context4ai.org/sourcegraph/auth/github/callback`.

## Create Google credentials

1. Create or select a project in Google Cloud Console. Configure Google Auth Platform branding, audience and contact details.
2. Create an OAuth client with application type **Web application**.
3. Add the Google callback above as an authorized redirect URI. The service uses `openid`, `profile` and `email` scopes.
4. If the consent screen is in testing mode, add the administrator's Google account as a test user.
5. Store the client ID and secret as `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET` in Fly Secrets. Redeploy/restart and sign in.

[Google's web application guide](https://developers.google.com/identity/protocols/oauth2/web-server)

## Create GitHub credentials

1. In GitHub developer settings, open **OAuth Apps → New OAuth App**. This is user login, not a GitHub Actions OIDC setup.
2. Use the demo service URL as Homepage URL and the GitHub callback above as Authorization callback URL.
3. Generate a client secret and store `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET` in Fly Secrets.
4. Redeploy/restart, then sign in with the owner account. Only the `read:user` scope is requested; repository access credentials remain separate.

[GitHub's OAuth app guide](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app)

Use the Fly dashboard Secrets UI, or `fly secrets import --app context4ai-sourcegraph` with a private input stream. Do not paste secrets into chat, commit them, or leave them in shell history. If both providers are configured, the first one used owns the administrator account; the other provider creates a distinct identity and is subject to registration policy.
