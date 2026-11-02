// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group may give an unanswered non-final member a deadline. The
// setting round-trips in seconds, reasoning does not count, and its card says
// when it will move on. Covered in five languages, both engines and widths.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "oa/gpt-6.1-sol", name: "gpt-6.1-sol", providerName: "OpenAI", icon: "generic" },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic" },
];
const members = models.map((m) => m.id);
const saved = { firstToken: 0, answerTimeout: 0 };
const groups = () => ({ models, pools: [], groups: [{ id: "sol", name: "Sol", members, routing: "order", ready: true,
  firstToken: saved.firstToken, answerTimeout: saved.answerTimeout, memberInfo: members.map((id) => ({ id, ready: true })) }] });

const words = {
  en: { edit: "Edit", save: "Save", label: "No answer", wait: "Wait", min: "1 min", tag: "Next after 60 s without an answer", waits: "Every member is waited for" },
  zh: { edit: "编辑", save: "保存", label: "迟迟不回答", wait: "一直等", min: "1 分钟", tag: "60 秒 无回答换下一个", waits: "每个成员都会一直等" },
  "zh-TW": { edit: "編輯", save: "儲存", label: "遲遲不回答", wait: "一直等", min: "1 分鐘", tag: "60 秒 無回答換下一個", waits: "每個成員都會一直等" },
  ja: { edit: "編集", save: "保存", label: "回答がないとき", wait: "待つ", min: "1 分", tag: "60 秒 回答がなければ次へ", waits: "回答前の推論" },
  de: { edit: "Bearbeiten", save: "Speichern", label: "Keine Antwort", wait: "Warten", min: "1 Min.", tag: "Nächstes nach 60 s ohne Antwort", waits: "Auf jedes Mitglied wird gewartet" },
};
const custom = { en: "Custom", zh: "自定义", "zh-TW": "自定義", ja: "カスタム", de: "Benutzerdefiniert" };

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") return url.searchParams.get("wait") ? new Promise(() => {}) : json({ mine: true, now: new Date().toISOString(), seq: 1, totals: {}, routes: [] });
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      saved.firstToken = body.firstToken || 0;
      saved.answerTimeout = body.answerTimeout || 0;
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await r.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) for (const width of [1100, 420]) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}: a group moves on when no answer comes`, async (t) => {
      saved.firstToken = saved.answerTimeout = 0;
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width, height: 2000 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(6000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=routing");
      const card = page.locator(".rt-group", { hasText: "Sol" });
      await card.waitFor();
      const reach = async (loc) => {
        await loc.waitFor();
        await page.mouse.move(width / 2, 600);
        for (let i = 0; i < 30; i++) {
          const b = await loc.boundingBox();
          if (b && b.y >= 0 && b.y + b.height <= 2000) return loc;
          await page.mouse.wheel(0, b && b.y < 0 ? -400 : 400);
          await page.waitForTimeout(30);
        }
        return loc;
      };
      const open = async () => (await reach(card.locator("button", { hasText: w.edit }))).click();
      const save = async () => {
        await (await reach(page.locator(".rt-gedit button.primary", { hasText: w.save }))).click();
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        return posts.filter((p) => p.path === "/api/groups/save").at(-1).body;
      };
      await open();
      const row = page.locator(".rt-gedit label", { hasText: w.label }).locator("xpath=following-sibling::div[1]");
      await reach(row);
      assert.equal(await row.locator(".opt.on").textContent(), w.wait);
      assert((await row.locator(".hint").textContent()).startsWith(w.waits));
      await row.locator(".opt", { hasText: w.min }).click();
      assert.equal((await save()).answerTimeout, 60);
      await card.locator(".tag", { hasText: w.tag }).waitFor();

      await open();
      await reach(row);
      assert.equal(await row.locator(".opt.on").textContent(), w.min);
      await row.locator(".opt", { hasText: custom[lang] }).click();
      const minutes = row.locator("input.answer-in");
      await minutes.fill("2.5");
      assert.equal(await minutes.getAttribute("aria-invalid"), "false");
      assert.equal((await save()).answerTimeout, 150);

      await open();
      await reach(row);
      assert.equal(await minutes.inputValue(), "2.5");
      await row.locator(".opt", { hasText: w.wait }).click();
      assert.equal((await save()).answerTimeout, 0);
      assert.deepEqual(errors, []);
    });
  }
}

test("every group save preserves answerTimeout and every locale has its copy", async () => {
  const client = await fs.readFile(path.join(assets, "plugins/routing/client.js"), "utf8");
  for (const literal of client.match(/groupAction\("save", \{[^\n]+/g) || []) {
    if (literal.includes("firstToken:")) assert(literal.includes("answerTimeout:"), literal);
  }
  const source = await fs.readFile(path.join(assets, "plugins/routing/i18n.js"), "utf8");
  const dictionaries = Function(`let out; const magpie = { strings: (x) => { out = x; } };\n${source}\nreturn out`)();
  for (const key of [
    "No answer", "Next after {n} without an answer",
    "Every member is waited for, however long it reasons before answering.",
    "A member that still has no answer text or tool call after {n} is let go and the next one asked. Reasoning does not count as an answer, and none of that member's output reaches the agent. It does not rest for this. The last member is always waited for.",
  ]) for (const lang of ["zh", "zh-TW", "ja", "de"]) assert(dictionaries[lang][key], `${lang}: ${key}`);
});
