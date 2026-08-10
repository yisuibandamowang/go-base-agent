const form = document.querySelector("#query-form");
const statusEl = document.querySelector("#status");
const resetBtn = document.querySelector("#reset-btn");
const resolveBtn = document.querySelector("#resolve-btn");
const prettyOutput = document.querySelector("#pretty-output");
const rawOutput = document.querySelector("#raw-output");
const commandOutput = document.querySelector("#command-output");
const envSelect = document.querySelector("#env-select");
const serviceSelect = document.querySelector("#service-select");
const deploymentSelect = document.querySelector("#deployment-select");
const podSelect = document.querySelector("#pod-select");
const analysisStatus = document.querySelector("#analysis-status");
const analysisOutput = document.querySelector("#analysis-output");

const summaryTarget = document.querySelector("#summary-target");
const summaryStdout = document.querySelector("#summary-stdout");
const summaryFile = document.querySelector("#summary-file");
let streamedAnalysisText = false;

function setStatus(text, tone = "idle") {
  statusEl.textContent = text;
  statusEl.dataset.tone = tone;
}

function formPayload() {
  const data = new FormData(form);
  const keywords = String(data.get("keywords") || "")
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
  return {
    env: String(data.get("env") || "").trim(),
    service: String(data.get("service") || "").trim(),
    pod: String(data.get("pod") || "").trim(),
    deployment: String(data.get("deployment") || "").trim(),
    question: String(data.get("question") || "").trim(),
    at: String(data.get("at") || "").trim(),
    before_minutes: Number(data.get("before_minutes") || 0),
    after_minutes: Number(data.get("after_minutes") || 0),
    keywords,
    all_pods: Boolean(data.get("all_pods")),
    include_critical: Boolean(data.get("include_critical")),
    include_gz: Boolean(data.get("include_gz")),
    resolve_only: Boolean(data.get("resolve_only")),
  };
}

function backendBase() {
  return document.querySelector("#backend-url").value.replace(/\/$/, "");
}

function backendURL(path) {
  return `${backendBase()}${path}`;
}

function setOptions(select, items, allLabel) {
  const current = select.value;
  select.innerHTML = "";
  const all = document.createElement("option");
  all.value = "all";
  all.textContent = allLabel;
  select.appendChild(all);
  for (const item of items || []) {
    if (!item || item.value === "all") continue;
    const option = document.createElement("option");
    option.value = item.value || item;
    option.textContent = item.label || item.value || item;
    select.appendChild(option);
  }
  if ([...select.options].some((item) => item.value === current)) {
    select.value = current;
  }
}

function formatPretty(result) {
  const summary = result.summary || {};
  const raw = result.raw || {};
  const lines = [];
  lines.push(`trace_id: ${result.trace_id || "-"}`);
  lines.push(`target: ${summary.target || "-"}`);
  lines.push(`stdout_lines: ${summary.stdout_lines || 0}`);
  lines.push(`file_log_lines: ${summary.file_log_lines || 0}`);
  if (Array.isArray(summary.log_files) && summary.log_files.length) {
    lines.push("");
    lines.push("log_files:");
    for (const file of summary.log_files) lines.push(`  - ${file}`);
  }
  if (Array.isArray(summary.errors) && summary.errors.length) {
    lines.push("");
    lines.push("errors:");
    for (const err of summary.errors) lines.push(`  - ${err}`);
  }
  const stdoutLines = raw.stdout?.lines || [];
  if (stdoutLines.length) {
    lines.push("");
    lines.push("stdout:");
    for (const item of stdoutLines.slice(0, 40)) lines.push(String(item));
  }
  const fileLogs = raw.fileLogs || [];
  for (const fileLog of fileLogs) {
    const fileLines = fileLog.lines || [];
    if (!fileLines.length) continue;
    lines.push("");
    lines.push(`file: ${fileLog.file || "-"}`);
    for (const item of fileLines.slice(0, 80)) lines.push(String(item));
  }
  const batchResults = raw.results || [];
  for (const result of batchResults) {
    lines.push("");
    lines.push(`[${result.env || "-"} / ${result.service || "-"}]`);
    if (result.error) {
      lines.push(`error: ${result.error}`);
      continue;
    }
    const nestedRaw = result.raw || {};
    const nestedStdout = nestedRaw.stdout?.lines || [];
    for (const item of nestedStdout.slice(0, 20)) lines.push(String(item));
    for (const fileLog of nestedRaw.fileLogs || []) {
      const fileLines = fileLog.lines || [];
      if (!fileLines.length) continue;
      lines.push(`file: ${fileLog.file || "-"}`);
      for (const item of fileLines.slice(0, 40)) lines.push(String(item));
    }
  }
  if (lines.length <= 4) lines.push("", "未返回匹配日志。");
  return lines.join("\n");
}

function renderResult(result, options = {}) {
  const summary = result.summary || {};
  summaryTarget.textContent = summary.target || "-";
  summaryStdout.textContent = String(summary.stdout_lines || 0);
  summaryFile.textContent = String(summary.file_log_lines || 0);
  prettyOutput.textContent = formatPretty(result);
  rawOutput.textContent = JSON.stringify(result.raw || result, null, 2);
  commandOutput.textContent = formatCommand(result.command || []);
  if (options.renderAnalysis !== false) {
    renderAnalysis(result.analysis);
  }
}

function renderAnalysis(analysis) {
  if (!analysis) {
    analysisStatus.textContent = "idle";
    analysisOutput.textContent = "";
    return;
  }
  if (analysis.error) {
    analysisStatus.textContent = "error";
    analysisOutput.textContent = analysis.error;
    return;
  }
  analysisStatus.textContent = "ready";
  const lines = [];
  if (analysis.content) lines.push(analysis.content);
  if (Array.isArray(analysis.code_evidence) && analysis.code_evidence.length) {
    lines.push("", "代码线索:");
    for (const item of analysis.code_evidence) {
      const pos = item.line ? `${item.file}:${item.line}` : item.file;
      lines.push(`- ${pos} ${item.content || ""}`.trim());
    }
  }
  analysisOutput.textContent = lines.join("\n") || "未返回分析内容。";
}

function appendOutput(target, text) {
  if (!text) return;
  target.textContent += text;
  target.scrollTop = target.scrollHeight;
}

function appendOutputLine(target, text) {
  appendOutput(target, `${target.textContent ? "\n" : ""}${text}`);
}

function formatCommand(command) {
  if (!Array.isArray(command)) return "";
  if (command.length && command.every((item) => typeof item === "string" && item.startsWith("node "))) {
    return command.join("\n\n");
  }
  return command.join(" ");
}

async function readEventStream(response, onEvent) {
  if (!response.body) throw new Error("当前浏览器不支持流式响应读取");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const frames = buffer.split(/\n\n/);
    buffer = frames.pop() || "";
    for (const frame of frames) {
      const event = parseSSEFrame(frame);
      if (event) onEvent(event);
    }
  }
  buffer += decoder.decode();
  const event = parseSSEFrame(buffer);
  if (event) onEvent(event);
}

function parseSSEFrame(frame) {
  let eventName = "message";
  const dataLines = [];
  for (const rawLine of frame.split(/\n/)) {
    const line = rawLine.trimEnd();
    if (line.startsWith("event:")) {
      eventName = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice(5).trimStart());
    }
  }
  if (!dataLines.length) return null;
  const dataText = dataLines.join("\n");
  const data = JSON.parse(dataText);
  return { event: eventName, data };
}

function handleStreamEvent(eventName, data) {
  const type = data.type || eventName;
  if (type === "progress") {
    appendOutputLine(prettyOutput, data.message || "日志查询进行中");
    return;
  }
  if (type === "log_result") {
    renderResult(data.result || {}, { renderAnalysis: false });
    appendOutputLine(analysisOutput, "日志已返回，开始结合代码链路分析。");
    return;
  }
  if (type === "analysis_progress") {
    analysisStatus.textContent = "running";
    appendOutputLine(analysisOutput, data.message || "分析进行中");
    return;
  }
  if (type === "code_evidence") {
    const evidence = data.code_evidence || [];
    appendOutputLine(analysisOutput, data.message || `代码线索检索完成，共 ${evidence.length} 条`);
    if (evidence.length) {
      appendOutputLine(analysisOutput, "代码线索:");
      for (const item of evidence.slice(0, 20)) {
        const pos = item.line ? `${item.file}:${item.line}` : item.file;
        appendOutputLine(analysisOutput, `- ${pos} ${item.content || ""}`.trim());
      }
      appendOutputLine(analysisOutput, "");
    }
    return;
  }
  if (type === "analysis_delta") {
    analysisStatus.textContent = "running";
    streamedAnalysisText = true;
    appendOutput(analysisOutput, data.delta || "");
    return;
  }
  if (type === "analysis_result") {
    analysisStatus.textContent = data.analysis?.error ? "error" : "ready";
    if (data.analysis?.content && !streamedAnalysisText) {
      appendOutputLine(analysisOutput, data.analysis.content);
    }
    if (data.analysis?.error) {
      appendOutputLine(analysisOutput, data.analysis.error);
    }
    return;
  }
  if (type === "error") {
    setStatus("error", "error");
    analysisStatus.textContent = "error";
    appendOutputLine(prettyOutput, data.error || "查询失败");
    appendOutputLine(analysisOutput, data.error || "分析失败");
    return;
  }
  if (type === "done") {
    if (statusEl.dataset.tone !== "error") setStatus("ready", "ready");
    if (analysisStatus.textContent === "running") analysisStatus.textContent = "ready";
  }
}

async function loadOptions() {
  try {
    const res = await fetch(backendURL("/api/log-agent/options"));
    const body = await res.json();
    setOptions(envSelect, body.envs, "全部环境");
    setOptions(serviceSelect, body.services, "全部服务");
  } catch (err) {
    console.warn("load options failed", err);
  }
}

function targetItemsFromResolve(raw) {
  if (Array.isArray(raw.targets)) return raw.targets;
  if (raw.target) return [raw.target];
  return [];
}

async function refreshResources() {
  const payload = formPayload();
  if (payload.env === "all" || payload.service === "all") {
    setOptions(deploymentSelect, [], "全部 Deployment");
    setOptions(podSelect, [], "全部 Pod");
    return;
  }
  setStatus("resolving", "running");
  const res = await fetch(backendURL("/api/log-agent/logs/search"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...payload, deployment: "", pod: "", resolve_only: true, all_pods: true, keywords: [] }),
  });
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  const deployment = body.raw?.resolution?.deployment;
  setOptions(deploymentSelect, deployment ? [{ value: deployment, label: deployment }] : [], "全部 Deployment");
  const pods = targetItemsFromResolve(body.raw).map((item) => ({ value: item.pod, label: item.pod }));
  setOptions(podSelect, pods, "全部 Pod");
  renderResult(body);
  setStatus("ready", "ready");
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  setStatus("running", "running");
  prettyOutput.textContent = "等待后端响应...";
  rawOutput.textContent = "";
  commandOutput.textContent = "";
  analysisStatus.textContent = "running";
  analysisOutput.textContent = "等待日志结果...";
  streamedAnalysisText = false;
  try {
    const res = await fetch(backendURL("/api/log-agent/logs/search/stream"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(formPayload()),
    });
    if (!res.ok) {
      const body = await res.json();
      throw new Error(body.error || `HTTP ${res.status}`);
    }
    prettyOutput.textContent = "";
    analysisOutput.textContent = "";
    await readEventStream(res, ({ event: eventName, data }) => handleStreamEvent(eventName, data));
    if (statusEl.dataset.tone !== "error") setStatus("ready", "ready");
    if (analysisStatus.textContent === "running") analysisStatus.textContent = "ready";
  } catch (err) {
    prettyOutput.textContent = String(err.message || err);
    analysisStatus.textContent = "error";
    analysisOutput.textContent = "";
    setStatus("error", "error");
  }
});

resolveBtn.addEventListener("click", async () => {
  try {
    await refreshResources();
  } catch (err) {
    prettyOutput.textContent = String(err.message || err);
    setStatus("error", "error");
  }
});

resetBtn.addEventListener("click", () => {
  form.reset();
  document.querySelector("#backend-url").value = "http://localhost:9108";
  setOptions(deploymentSelect, [], "全部 Deployment");
  setOptions(podSelect, [], "全部 Pod");
  summaryTarget.textContent = "-";
  summaryStdout.textContent = "0";
  summaryFile.textContent = "0";
  prettyOutput.textContent = "";
  rawOutput.textContent = "";
  commandOutput.textContent = "";
  analysisStatus.textContent = "idle";
  analysisOutput.textContent = "";
  setStatus("idle");
});

document.querySelectorAll(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach((item) => item.classList.remove("active"));
    document.querySelectorAll(".output").forEach((item) => item.classList.remove("active"));
    tab.classList.add("active");
    document.querySelector(`#${tab.dataset.tab}-output`).classList.add("active");
  });
});

loadOptions();
