// The reference example's frontend: a static app on the generated client
// (../w17/web-client), shipped as a container like every other part of the
// project (web/Dockerfile, the `web` workload in w17/infra.yaml, the `web`
// service in compose.reference.yaml). It signs in, creates a task and lists
// the caller's tasks — enough to prove the wiring: the API address baked in at
// build time, CORS on the gateway, DPoP-bound sessions.
import { Client } from '../../w17/web-client/index.js';

// The API's address is fixed when the image is built (VITE_API_URL): the
// bundle is static, so nothing can be read from the server at run time.
const apiUrl = import.meta.env.VITE_API_URL as string | undefined;
const app = document.querySelector<HTMLElement>('#app')!;

if (!apiUrl) {
  app.textContent = 'VITE_API_URL was not set when this bundle was built.';
  throw new Error('VITE_API_URL is not set');
}

const client = new Client({ baseUrl: apiUrl });
const session = client.newSession();

function render(html: string) {
  app.innerHTML = html;
}

function showError(err: unknown) {
  const p = document.createElement('p');
  p.style.color = 'crimson';
  p.textContent = err instanceof Error ? err.message : String(err);
  app.append(p);
}

function signInForm() {
  render(`
    <h1>Tasks</h1>
    <form id="auth">
      <input name="email" type="email" placeholder="email" required />
      <input name="password" type="password" placeholder="password" required minlength="12" />
      <button name="mode" value="in">Sign in</button>
      <button name="mode" value="up">Sign up</button>
    </form>`);
  const form = document.querySelector<HTMLFormElement>('#auth')!;
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const data = new FormData(form);
    const mode = (e.submitter as HTMLButtonElement | null)?.value;
    const creds = { email: String(data.get('email')), password: String(data.get('password')) };
    try {
      const resp = mode === 'up' ? await session.auth.signUp(creds) : await session.auth.signIn(creds);
      client.bearerLogin(resp.token, { publicComputer: resp.publicComputer });
      await tasksPage();
    } catch (err) {
      showError(err);
    }
  });
}

async function tasksPage() {
  const page = await session.tasks.listPaged({ limit: 50 });
  render(`
    <h1>Tasks</h1>
    <form id="add"><input name="title" placeholder="a new task" required /><button>Add</button></form>
    <ul>${page.items.map((t) => `<li>${escapeHtml(t.title)}</li>`).join('')}</ul>`);
  const form = document.querySelector<HTMLFormElement>('#add')!;
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      await session.tasks.create({ title: String(new FormData(form).get('title')) });
      await tasksPage();
    } catch (err) {
      showError(err);
    }
  });
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
}

signInForm();
