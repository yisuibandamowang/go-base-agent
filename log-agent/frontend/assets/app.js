const form = document.querySelector("#query-form");
const statusEl = document.querySelector("#status");
const resetBtn = document.querySelector("#reset-btn");
const resolveBtn = document.querySelector("#resolve-btn");
const diagnosisBtn = document.querySelector("#diagnosis-btn");
const stopBtn = document.querySelector("#stop-btn");
const submitBtn = form.querySelector('button[type="submit"]');
const prettyOutput = document.querySelector("#pretty-output");
const rawOutput = document.querySelector("#raw-output");
const commandOutput = document.querySelector("#command-output");
const projectSelect = document.querySelector("#project-select");
const envSelect = document.querySelector("#env-select");
const serviceSelect = document.querySelector("#service-select");
const deploymentSelect = document.querySelector("#deployment-select");
const podSelect = document.querySelector("#pod-select");
const analysisStatus = document.querySelector("#analysis-status");
const analysisOutput = document.querySelector("#analysis-output");
const sqlLocationPanel = document.querySelector("#sql-location-panel");
const sqlLocationWrite = document.querySelector("#sql-location-write");
const sqlLocationTable = document.querySelector("#sql-location-table");
const sqlLocationFields = document.querySelector("#sql-location-fields");

const summaryTarget = document.querySelector("#summary-target");
const summaryStdout = document.querySelector("#summary-stdout");
const summaryFile = document.querySelector("#summary-file");
let streamedAnalysisText = false;
let activeController = null;
let deploymentsByProject = {};
let envOptions = [];
let currentSQLLocation = null;
const defaultCodeRepoPaths = {
  member: "/Users/work_project/360/member",
  fuyao: "/Users/work_project/360/ad-platform-bot",
};

function setStatus(text, tone = "idle") {
  statusEl.textContent = text;
  statusEl.dataset.tone = tone;
}

function setRunning(running) {
  submitBtn.disabled = running;
  diagnosisBtn.disabled = running;
  stopBtn.disabled = !running;
  resolveBtn.disabled = running;
  resetBtn.disabled = running;
}

function formPayload() {
  const data = new FormData(form);
  const keywords = String(data.get("keywords") || "")
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
  const payload = {
    project: String(data.get("project") || "member").trim(),
    env: String(data.get("env") || "").trim(),
    service: String(data.get("service") || "").trim(),
    code_repo_path: String(data.get("code_repo_path") || "").trim(),
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
  if (payload.deployment === "all") payload.deployment = "";
  if (payload.pod === "all") payload.pod = "";
  if (payload.deployment && payload.deployment !== "all") {
    if (payload.env === "all") payload.env = "";
    if (payload.service === "all") payload.service = "";
  }
  if (payload.pod && payload.pod !== "all") {
    if (payload.env === "all") payload.env = "";
    if (payload.service === "all") payload.service = "";
    if (payload.deployment === "all") payload.deployment = "";
  }
  return payload;
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

function refreshDeploymentOptions() {
  const project = projectSelect.value || "member";
  const items = deploymentsByProject[project] || [];
  setOptions(deploymentSelect, items, "全部 Deployment");
  if (project === "fuyao") {
    deploymentSelect.value = "all";
    syncFuyaoDeploymentWithEnv();
  }
}

function refreshEnvOptions() {
  const project = projectSelect.value || "member";
  const items = project === "fuyao"
    ? envOptions.filter((item) => ["all", "test", "regress", "online"].includes(item.value))
    : envOptions;
  setOptions(envSelect, items, "全部环境");
}

function syncCodeRepoPathWithProject() {
  const project = projectSelect.value || "member";
  const codeRepoPath = document.querySelector("#code-repo-path");
  codeRepoPath.value = defaultCodeRepoPaths[project] || defaultCodeRepoPaths.member;
}

function fuyaoDeploymentForEnv(env) {
  if (env === "test") return "ad-platform-test";
  if (env === "regress") return "ad-platform-regress";
  if (env === "online") return "ad-platform-online";
  return "";
}

function syncFuyaoDeploymentWithEnv() {
  if ((projectSelect.value || "member") !== "fuyao") return;
  if (deploymentSelect.value && deploymentSelect.value !== "all") return;
  const deployment = fuyaoDeploymentForEnv(envSelect.value);
  if (!deployment) {
    deploymentSelect.value = "all";
    return;
  }
  if ([...deploymentSelect.options].some((option) => option.value === deployment)) {
    deploymentSelect.value = deployment;
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
    const nestedRaw = result.raw || result;
    const target = nestedRaw.target || {};
    const nestedStdout = nestedRaw.stdout?.lines || [];
    const nestedFileLogs = (nestedRaw.fileLogs || []).filter((fileLog) => (fileLog.lines || []).length || fileLog.error);
    if (!result.error && !nestedStdout.length && !nestedFileLogs.length) continue;
    lines.push("");
    lines.push(`[${result.env || target.env || "-"} / ${result.service || target.service || "-"} / ${target.pod || "-"}]`);
    if (result.error) {
      lines.push(`error: ${result.error}`);
      continue;
    }
    for (const item of nestedStdout.slice(0, 20)) lines.push(String(item));
    for (const fileLog of nestedFileLogs) {
      const fileLines = fileLog.lines || [];
      if (fileLog.error) lines.push(`error: ${fileLog.error}`);
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
    updateSQLLocation(null);
    return;
  }
  updateSQLLocation(analysis.sql_location || null);
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

function formatSQLLocationValue(value, fallback = "-") {
  if (Array.isArray(value)) {
    return value.length ? value.join(", ") : fallback;
  }
  if (typeof value === "string" && value.trim()) {
    return value.trim();
  }
  return fallback;
}

function updateSQLLocation(location) {
  currentSQLLocation = location || null;
  if (!sqlLocationPanel || !sqlLocationWrite || !sqlLocationTable || !sqlLocationFields) return;
  if (!location) {
    sqlLocationPanel.hidden = true;
    sqlLocationWrite.textContent = "-";
    sqlLocationTable.textContent = "-";
    sqlLocationFields.textContent = "-";
    return;
  }
  sqlLocationPanel.hidden = false;
  sqlLocationWrite.textContent = formatSQLLocationValue(location.write_point);
  sqlLocationTable.textContent = formatSQLLocationValue(location.table);
  sqlLocationFields.textContent = formatSQLLocationValue(location.fields);
}

function formatDBResult(result) {
  if (!result) return "数据库未返回结果。";
  const lines = [];
  lines.push(`SQL: ${result.sql || "-"}`);
  lines.push(`rows: ${result.row_count || 0}`);
  const rows = result.rows || [];
  for (const row of rows.slice(0, 20)) {
    lines.push(JSON.stringify(row));
  }
  return lines.join("\n");
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
  if (type === "sql_location") {
    updateSQLLocation(data.sql_location || null);
    appendOutputLine(analysisOutput, data.message || "SQL 定位完成");
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
    updateSQLLocation(data.analysis?.sql_location || currentSQLLocation);
    if (data.analysis?.content && !streamedAnalysisText) {
      appendOutputLine(analysisOutput, data.analysis.content);
    }
    if (data.analysis?.error) {
      appendOutputLine(analysisOutput, data.analysis.error);
    }
    return;
  }
  if (type === "db_schema_progress") {
    appendOutputLine(analysisOutput, data.message || "开始数据库查询");
    return;
  }
  if (type === "db_query_result") {
    appendOutputLine(analysisOutput, "");
    appendOutputLine(analysisOutput, "数据库查询:");
    if (data.error) {
      appendOutputLine(analysisOutput, data.error);
    } else if (data.db_result) {
      appendOutputLine(analysisOutput, formatDBResult(data.db_result));
    } else {
      appendOutputLine(analysisOutput, data.message || "数据库查询完成");
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
    envOptions = body.envs || [];
    refreshEnvOptions();
    setOptions(serviceSelect, body.services, "全部服务");
    deploymentsByProject = body.deployments || {};
    refreshDeploymentOptions();
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
  if (!payload.deployment && (payload.env === "all" || payload.service === "all")) {
    setOptions(podSelect, [], "全部 Pod");
    return;
  }
  setStatus("resolving", "running");
  const res = await fetch(backendURL("/api/log-agent/logs/search"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...payload, pod: "", resolve_only: true, all_pods: true, keywords: [] }),
  });
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  const deployment = body.raw?.resolution?.deployment;
  if (deployment && ![...deploymentSelect.options].some((option) => option.value === deployment)) {
    const option = document.createElement("option");
    option.value = deployment;
    option.textContent = deployment;
    deploymentSelect.appendChild(option);
  }
  if (deployment) deploymentSelect.value = deployment;
  const pods = targetItemsFromResolve(body.raw).map((item) => ({ value: item.pod, label: item.pod }));
  setOptions(podSelect, pods, "全部 Pod");
  renderResult(body);
  setStatus("ready", "ready");
}

async function runStreamSearch(path, payload) {
  if (activeController) return;
  activeController = new AbortController();
  setRunning(true);
  setStatus("running", "running");
  prettyOutput.textContent = "等待后端响应...";
  rawOutput.textContent = "";
  commandOutput.textContent = "";
  analysisStatus.textContent = "running";
  analysisOutput.textContent = "等待日志结果...";
  updateSQLLocation(null);
  streamedAnalysisText = false;
  try {
    const res = await fetch(backendURL(path), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: activeController.signal,
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
    if (err.name === "AbortError") {
      appendOutputLine(prettyOutput, "已停止本次查询。");
      appendOutputLine(analysisOutput, "已停止本次分析。");
      analysisStatus.textContent = "stopped";
      setStatus("stopped", "idle");
      return;
    }
    prettyOutput.textContent = String(err.message || err);
    analysisStatus.textContent = "error";
    analysisOutput.textContent = "";
    updateSQLLocation(null);
    setStatus("error", "error");
  } finally {
    activeController = null;
    setRunning(false);
  }
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    await runStreamSearch("/api/log-agent/logs/search/stream", formPayload());
  } catch (err) {
    prettyOutput.textContent = String(err.message || err);
    analysisStatus.textContent = "error";
    analysisOutput.textContent = "";
    setStatus("error", "error");
  }
});

diagnosisBtn.addEventListener("click", async () => {
  try {
    await runStreamSearch("/api/log-agent/diagnosis/search/stream", formPayload());
  } catch (err) {
    prettyOutput.textContent = String(err.message || err);
    analysisStatus.textContent = "error";
    analysisOutput.textContent = "";
    setStatus("error", "error");
  }
});

stopBtn.addEventListener("click", () => {
  if (!activeController) return;
  activeController.abort();
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
  projectSelect.value = "member";
  syncCodeRepoPathWithProject();
  refreshEnvOptions();
  refreshDeploymentOptions();
  setOptions(podSelect, [], "全部 Pod");
  summaryTarget.textContent = "-";
  summaryStdout.textContent = "0";
  summaryFile.textContent = "0";
  prettyOutput.textContent = "";
  rawOutput.textContent = "";
  commandOutput.textContent = "";
  analysisStatus.textContent = "idle";
  analysisOutput.textContent = "";
  updateSQLLocation(null);
  setStatus("idle");
});

projectSelect.addEventListener("change", () => {
  syncCodeRepoPathWithProject();
  refreshEnvOptions();
  refreshDeploymentOptions();
  setOptions(podSelect, [], "全部 Pod");
});

envSelect.addEventListener("change", () => {
  syncFuyaoDeploymentWithEnv();
  setOptions(podSelect, [], "全部 Pod");
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
