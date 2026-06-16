// flowManager Dashboard — client-side logic.
// No frameworks, no npm. Единственная зависимость — вендоренный markdown-it.min.js.

(function () {
    "use strict";

    // ---- DOM refs ----
    var $flowName = document.getElementById("flow-name");
    var $wsStatus = document.getElementById("ws-status");
    var $stagesList = document.getElementById("stages-list");
    var $detailEmpty = document.getElementById("detail-empty");
    var $detailContent = document.getElementById("detail-content");
    var $detailTitle = document.getElementById("detail-title");
    var $detailStatus = document.getElementById("detail-status");
    var $planSection = document.getElementById("plan-section");
    var $planContent = document.getElementById("plan-content");
    var $planEmpty = document.getElementById("plan-empty");
    var $actionsSection = document.getElementById("actions-section");
    var $btnApprove = document.getElementById("btn-approve");
    var $btnRevise = document.getElementById("btn-revise");
    var $commentHint = document.getElementById("comment-hint");
    var $logContent = document.getElementById("log-content");
    var $logEmpty = document.getElementById("log-empty");
    var $progressFill = document.getElementById("progress-fill");
    var $progressText = document.getElementById("progress-text");
    var $startedAt = document.getElementById("started-at");
    var $elapsed = document.getElementById("elapsed");
    var $feedContent = document.getElementById("feed-content");
    var $retrySection = document.getElementById("retry-section");
    var $btnRetry = document.getElementById("btn-retry");
    var $dialogSection = document.getElementById("dialog-section");
    var $dialogHistory = document.getElementById("dialog-history");
    var $dialogPending = document.getElementById("dialog-pending");
    var $dialogToggle = document.getElementById("dialog-toggle");

    // ---- State ----
    var state = null;          // RunState from API
    var selectedStageID = null;
    var ws = null;
    var reconnectDelay = 1000;
    var elapsedTimer = null;
    var logPollTimer = null;

    // Inline comments: Map<lineNumber, commentText>
    var lineComments = {};
    var activeCommentLine = null;

    // Dialog state
    var dialogState = { pending: null };
    var dialogEntries = [];

    // ---- Special section detection ----
    var SPECIAL_SECTIONS = {
        "## Assumptions": { css: "plan-section-assumptions", icon: "\u26A0", label: "Assumptions" },
        "## Acceptance Criteria": { css: "plan-section-criteria", icon: "\u2713", label: "Acceptance Criteria" }
    };

    function detectSection(line) {
        var trimmed = line.trim();
        for (var key in SPECIAL_SECTIONS) {
            if (trimmed === key) return SPECIAL_SECTIONS[key];
        }
        return null;
    }

    function isH2(line) {
        return line.trim().indexOf("## ") === 0;
    }

    // ---- Status labels (Russian) ----
    var statusLabels = {
        pending: "Ожидание",
        planning: "Планирование",
        awaiting_approval: "Ожидает одобрения",
        revising: "Правки",
        ready: "Готов",
        running: "Выполняется",
        done: "Завершено",
        failed: "Ошибка",
        retrying: "Повтор",
        awaiting_user_input: "Ожидает ответ"
    };

    // ---- API helpers ----
    function apiGet(path, cb) {
        var xhr = new XMLHttpRequest();
        xhr.open("GET", path, true);
        xhr.onreadystatechange = function () {
            if (xhr.readyState !== 4) return;
            if (xhr.status >= 200 && xhr.status < 300) {
                cb(null, xhr.responseText);
            } else {
                cb(new Error("GET " + path + " -> " + xhr.status));
            }
        };
        xhr.send();
    }

    function apiPost(path, body, cb) {
        var xhr = new XMLHttpRequest();
        xhr.open("POST", path, true);
        xhr.setRequestHeader("Content-Type", "application/json");
        xhr.onreadystatechange = function () {
            if (xhr.readyState !== 4) return;
            if (xhr.status >= 200 && xhr.status < 300) {
                cb(null);
            } else {
                cb(new Error("POST " + path + " -> " + xhr.status));
            }
        };
        xhr.send(body ? JSON.stringify(body) : null);
    }

    // ---- Markdown helpers ----
    // Инлайн-рендер одной строки для построчного режима ревью:
    // bold, курсив, ссылки, code — через markdown-it; чекбоксы — свои спаны.
    function inlineFormat(text) {
        var html = md.renderInline(text);
        html = html.replace(/\[x\]/g, '<span class="cb cb-done">&#10003;</span>');
        html = html.replace(/\[ \]/g, '<span class="cb cb-open">&#9744;</span>');
        return html;
    }

    function escapeHTML(s) {
        return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    }

    var md = window.markdownit({ html: false, linkify: true });

    // Рендерит markdown в элемент: спецсекции, чекбоксы, ссылки в новой вкладке.
    function renderMarkdownInto(el, text) {
        el.classList.add("md");
        el.innerHTML = renderMarkdownHTML(text || "");
        decorateCheckboxes(el);
        decorateLinks(el);
    }

    // Режет текст по заголовкам спецсекций (## Assumptions / ## Acceptance Criteria),
    // каждый кусок рендерит markdown-it'ом, секции оборачивает в сворачиваемые блоки.
    function renderMarkdownHTML(text) {
        var lines = text.split("\n");
        var html = [];
        var buf = [];
        var inSection = false;
        var inCode = false;

        function flush() {
            if (buf.length) {
                html.push(md.render(buf.join("\n")));
                buf = [];
            }
        }

        for (var i = 0; i < lines.length; i++) {
            var line = lines[i];
            if (line.trim().indexOf("```") === 0) {
                inCode = !inCode;
                buf.push(line);
                continue;
            }
            if (inCode) {
                buf.push(line);
                continue;
            }
            var section = detectSection(line);
            if (section) {
                flush();
                if (inSection) html.push("</div></div>");
                html.push('<div class="plan-section-wrapper ' + section.css + '">' +
                    '<h2 class="section-header">' + section.icon + " " + section.label + "</h2>" +
                    '<div class="plan-section-body">');
                inSection = true;
                continue;
            }
            if (inSection && isH2(line)) {
                flush();
                html.push("</div></div>");
                inSection = false;
            }
            buf.push(line);
        }
        flush();
        if (inSection) html.push("</div></div>");
        return html.join("");
    }

    // Заменяет [x] / [ ] в начале пунктов списка на стилизованные чекбоксы.
    function decorateCheckboxes(root) {
        root.querySelectorAll("li").forEach(function (li) {
            var html = li.innerHTML;
            if (/^\s*\[x\]/.test(html)) {
                li.innerHTML = html.replace("[x]", '<span class="cb cb-done">&#10003;</span>');
            } else if (/^\s*\[ \]/.test(html)) {
                li.innerHTML = html.replace("[ ]", '<span class="cb cb-open">&#9744;</span>');
            }
        });
    }

    // Внешние ссылки открываются в новой вкладке, чтобы не терять состояние дашборда.
    function decorateLinks(root) {
        root.querySelectorAll("a").forEach(function (a) {
            a.target = "_blank";
            a.rel = "noopener";
        });
    }

    // ---- Render plan with line numbers and inline comments ----
    function renderPlanReview(text) {
        $planContent.innerHTML = "";
        $planContent.classList.remove("md");
        $planEmpty.classList.add("hidden");
        $planContent.classList.remove("hidden");

        if (!text || !text.trim()) {
            $planEmpty.classList.remove("hidden");
            $planContent.classList.add("hidden");
            return;
        }

        var lines = text.split("\n");
        var fragment = document.createDocumentFragment();
        var currentWrapper = null; // active special section wrapper
        var currentBody = null;    // body div inside wrapper

        var inCodeBlock = false;
        var codeBlockStart = 0;
        var codeLines = [];

        for (var i = 0; i < lines.length; i++) {
            var line = lines[i];
            var lineNum = i + 1;

            // Handle code blocks
            if (line.trim().indexOf("```") === 0) {
                if (!inCodeBlock) {
                    inCodeBlock = true;
                    codeBlockStart = i;
                    codeLines = [line];
                    continue;
                } else {
                    inCodeBlock = false;
                    codeLines.push(line);
                    var codeDiv = createPlanLine(codeBlockStart + 1, codeLines.join("\n").length, "<pre><code>" + escapeHTML(codeLines.join("\n").trim()) + "</code></pre>");
                    (currentBody || fragment).appendChild(codeDiv);
                    continue;
                }
            }

            if (inCodeBlock) {
                codeLines.push(line);
                continue;
            }

            // Check for special section start
            var section = detectSection(line);
            if (section) {
                // Close previous wrapper if any
                if (currentWrapper) {
                    fragment.appendChild(currentWrapper);
                    currentWrapper = null;
                    currentBody = null;
                }
                currentWrapper = document.createElement("div");
                currentWrapper.className = "plan-section-wrapper " + section.css;

                var header = document.createElement("h2");
                header.className = "section-header";
                header.innerHTML = section.icon + " " + section.label + ' <span class="toggle">\u25BE</span>';
                header.addEventListener("click", (function (w) {
                    return function () { w.classList.toggle("collapsed"); };
                })(currentWrapper));

                currentBody = document.createElement("div");
                currentBody.className = "plan-section-body";

                currentWrapper.appendChild(header);
                currentWrapper.appendChild(currentBody);
                continue;
            }

            // If we hit another ## while inside a special section, close the wrapper
            if (currentWrapper && isH2(line)) {
                fragment.appendChild(currentWrapper);
                currentWrapper = null;
                currentBody = null;
            }

            // Regular line
            var html = formatLine(line);
            var div = createPlanLine(lineNum, lineNum, html);
            (currentBody || fragment).appendChild(div);
        }

        // Close unclosed wrapper
        if (currentWrapper) {
            fragment.appendChild(currentWrapper);
        }

        // Unclosed code block
        if (inCodeBlock && codeLines.length > 0) {
            var codeDiv2 = createPlanLine(codeBlockStart + 1, lines.length, "<pre><code>" + escapeHTML(codeLines.join("\n")) + "</code></pre>");
            fragment.appendChild(codeDiv2);
        }

        $planContent.appendChild(fragment);
        decorateLinks($planContent);
        updateReviseButton();
    }

    function formatLine(line) {
        // heading
        if (line.indexOf("### ") === 0) return "<h3>" + inlineFormat(line.slice(4)) + "</h3>";
        if (line.indexOf("## ") === 0) return "<h2>" + inlineFormat(line.slice(3)) + "</h2>";
        if (line.indexOf("# ") === 0) return "<h1>" + inlineFormat(line.slice(2)) + "</h1>";

        // list item
        if (/^[-*]\s+/.test(line)) return "<li>" + inlineFormat(line.replace(/^[-*]\s+/, "")) + "</li>";

        // empty
        if (line.trim() === "") return "&nbsp;";

        // paragraph
        return "<p>" + inlineFormat(line) + "</p>";
    }

    function createPlanLine(startLine, endLine, htmlContent) {
        var div = document.createElement("div");
        div.className = "plan-line";
        div.dataset.line = startLine;
        div.dataset.lineEnd = endLine;

        var numSpan = document.createElement("span");
        numSpan.className = "line-num";
        numSpan.textContent = startLine;

        var contentSpan = document.createElement("span");
        contentSpan.className = "line-content";
        contentSpan.innerHTML = htmlContent;

        var marker = document.createElement("span");
        marker.className = "line-comment-marker";
        marker.textContent = "\u25CF"; // bullet dot

        div.appendChild(numSpan);
        div.appendChild(contentSpan);
        div.appendChild(marker);

        // Check if this line already has a comment
        if (lineComments[startLine]) {
            div.classList.add("has-comment");
            appendCommentDisplay(div, startLine, lineComments[startLine]);
        }

        div.addEventListener("click", onPlanLineClick);
        return div;
    }

    function onPlanLineClick(e) {
        var lineDiv = e.currentTarget;
        var lineNum = parseInt(lineDiv.dataset.line, 10);

        // Only allow comments when stage is awaiting_approval
        if (!selectedStageID || !state) return;
        var st = state.stages[selectedStageID];
        if (!st || st.status !== "awaiting_approval") return;

        // If clicking the same line with an active form, close it
        if (activeCommentLine === lineNum) {
            closeCommentForm();
            return;
        }

        // Close any existing form
        closeCommentForm();

        // Open new form
        activeCommentLine = lineNum;
        var form = document.createElement("div");
        form.className = "line-comment-form";
        form.id = "active-comment-form";

        var textarea = document.createElement("textarea");
        textarea.placeholder = "Комментарий к строке " + lineNum + "...";
        textarea.value = lineComments[lineNum] || "";

        var actions = document.createElement("div");
        actions.className = "comment-actions";

        var btnAdd = document.createElement("button");
        btnAdd.className = "btn btn-send";
        btnAdd.textContent = lineComments[lineNum] ? "Обновить" : "Добавить";
        btnAdd.addEventListener("click", function (ev) {
            ev.stopPropagation();
            var text = textarea.value.trim();
            if (text) {
                lineComments[lineNum] = text;
                refreshPlanDisplay();
            } else {
                delete lineComments[lineNum];
                refreshPlanDisplay();
            }
            activeCommentLine = null;
        });

        var btnCancel = document.createElement("button");
        btnCancel.className = "btn btn-cancel";
        btnCancel.textContent = "Отмена";
        btnCancel.addEventListener("click", function (ev) {
            ev.stopPropagation();
            closeCommentForm();
            activeCommentLine = null;
        });

        var btnDelete = document.createElement("button");
        btnDelete.className = "btn btn-cancel";
        btnDelete.textContent = "Удалить";
        btnDelete.style.display = lineComments[lineNum] ? "inline-block" : "none";
        btnDelete.addEventListener("click", function (ev) {
            ev.stopPropagation();
            delete lineComments[lineNum];
            refreshPlanDisplay();
            activeCommentLine = null;
        });

        actions.appendChild(btnAdd);
        actions.appendChild(btnDelete);
        actions.appendChild(btnCancel);
        form.appendChild(textarea);
        form.appendChild(actions);

        // Prevent click propagation
        form.addEventListener("click", function (ev) { ev.stopPropagation(); });

        lineDiv.appendChild(form);
        textarea.focus();
    }

    function closeCommentForm() {
        var form = document.getElementById("active-comment-form");
        if (form) form.remove();
    }

    function appendCommentDisplay(lineDiv, lineNum, text) {
        // Remove old display if any
        var old = lineDiv.querySelector(".line-comment-display");
        if (old) old.remove();

        var display = document.createElement("div");
        display.className = "line-comment-form line-comment-display";
        display.innerHTML = '<div style="color:var(--c-awaiting);font-size:12px;margin-bottom:4px">Комментарий к строке ' + lineNum + '</div>' +
            '<div style="color:var(--text);white-space:pre-wrap">' + escapeHTML(text) + '</div>';
        display.addEventListener("click", function (ev) { ev.stopPropagation(); });
        lineDiv.appendChild(display);
    }

    function refreshPlanDisplay() {
        // Re-render plan with current comments
        apiGet("/api/stages/" + encodeURIComponent(selectedStageID) + "/plan", function (err, text) {
            renderPlanReview(err ? "" : text);
        });
    }

    function updateReviseButton() {
        var count = Object.keys(lineComments).length;
        $btnRevise.disabled = count === 0;
        $btnRevise.textContent = count > 0 ? "Отправить правку (" + count + ")" : "Отправить правку";
    }

    function buildFeedbackString() {
        var parts = [];
        var lineNums = Object.keys(lineComments).map(Number).sort(function (a, b) { return a - b; });
        for (var i = 0; i < lineNums.length; i++) {
            var n = lineNums[i];
            parts.push("Строка " + n + ": " + lineComments[n]);
        }
        return parts.join("\n\n");
    }

    // ---- Dialog ----
    function loadDialog(stageID) {
        apiGet("/api/stages/" + encodeURIComponent(stageID) + "/dialog", function (err, body) {
            if (err) { dialogEntries = []; renderDialog(stageID); return; }
            try { dialogEntries = JSON.parse(body); } catch (_) { dialogEntries = []; }
            renderDialog(stageID);
        });
    }

    function renderDialog(stageID) {
        var stageStatus = state && state.stages && state.stages[stageID] ? state.stages[stageID].status : "";
        var hasContent = dialogEntries.length > 0 || stageStatus === "awaiting_user_input";

        if (!hasContent) {
            $dialogSection.classList.add("hidden");
            return;
        }
        $dialogSection.classList.remove("hidden");

        // History: render Q/A pairs grouped by phase
        $dialogHistory.innerHTML = "";
        var currentPhase = "";
        dialogEntries.forEach(function (e) {
            if (e.phase !== currentPhase) {
                var div = document.createElement("div");
                div.className = "phase-divider";
                div.textContent = e.phase;
                $dialogHistory.appendChild(div);
                currentPhase = e.phase;
            }
            if (e.type === "agent_text") {
                var msg = document.createElement("div");
                msg.className = "agent-msg";
                renderMarkdownInto(msg, e.text);
                $dialogHistory.appendChild(msg);
                return;
            }
            if (e.answer !== undefined && e.answer !== null) {
                var qa = document.createElement("div");
                qa.className = "qa";
                var qDiv = document.createElement("div");
                qDiv.className = "q";
                renderMarkdownInto(qDiv, e.question);
                var aDiv = document.createElement("div");
                aDiv.className = "a";
                aDiv.textContent = "→ " + (e.answer || "");
                qa.appendChild(qDiv);
                qa.appendChild(aDiv);
                $dialogHistory.appendChild(qa);
            }
        });
        // Прокрутка к свежим сообщениям: длинные тексты агента (дизайн и т.п.)
        // иначе прячут актуальный контекст над панелью вопроса.
        $dialogHistory.scrollTop = $dialogHistory.scrollHeight;

        // Toggle visibility for collapsed history
        var closed = dialogEntries.filter(function (e) { return e.answer !== undefined && e.answer !== null; });
        if (closed.length > 0) {
            $dialogToggle.classList.remove("hidden");
        } else {
            $dialogToggle.classList.add("hidden");
        }

        // Pending: last open entry across all phases (agent_text is not a question)
        var open = null;
        for (var i = dialogEntries.length - 1; i >= 0; i--) {
            if (dialogEntries[i].type === "agent_text") continue;
            if (dialogEntries[i].answer === undefined || dialogEntries[i].answer === null) {
                open = dialogEntries[i];
                break;
            }
        }
        if (open) {
            renderPendingQuestion(stageID, open);
        } else {
            $dialogPending.classList.add("hidden");
            dialogState.pending = null;
        }
    }

    function renderPendingQuestion(stageID, q) {
        dialogState.pending = { stageID: stageID, id: q.id, phase: q.phase, allowCustom: q.allow_custom };

        $dialogPending.classList.remove("hidden");
        renderMarkdownInto($dialogPending.querySelector(".dialog-question"), q.question);

        var $opts = $dialogPending.querySelector(".dialog-options");
        $opts.innerHTML = "";
        $opts.classList.remove("dimmed");
        var selected = null;

        (q.options || []).forEach(function (opt, idx) {
            var btn = document.createElement("button");
            btn.type = "button";
            btn.textContent = opt;
            btn.style.animationDelay = (idx * 40) + "ms";
            btn.onclick = function () {
                selected = opt;
                Array.from($opts.querySelectorAll("button")).forEach(function (b) { b.classList.remove("selected"); });
                btn.classList.add("selected");
                $opts.classList.remove("dimmed");
                $dialogPending.querySelector(".dialog-custom").value = "";
            };
            $opts.appendChild(btn);
        });

        var $custom = $dialogPending.querySelector(".dialog-custom");
        $custom.value = "";
        $custom.disabled = !q.allow_custom;
        $custom.oninput = function () {
            if ($custom.value.length > 0) {
                $opts.classList.add("dimmed");
                Array.from($opts.querySelectorAll("button")).forEach(function (b) { b.classList.remove("selected"); });
                selected = null;
            } else {
                $opts.classList.remove("dimmed");
            }
        };

        $dialogPending.querySelector(".btn-send").onclick = function () {
            var customText = $custom.value.trim();
            var answer, fromOptions;
            if (customText.length > 0) {
                answer = customText;
                fromOptions = false;
            } else if (selected !== null) {
                answer = selected;
                fromOptions = true;
            } else {
                return;
            }
            sendAnswer(stageID, q.id, q.phase, answer, fromOptions);
        };

        $dialogPending.querySelector(".btn-cancel-dialog").onclick = function () {
            if (!confirm("Отменить стейдж?")) return;
            cancelDialog(stageID);
        };
    }

    function sendAnswer(stageID, qID, phase, answer, fromOptions) {
        apiPost("/api/stages/" + encodeURIComponent(stageID) + "/dialog/answer", {
            id: qID, phase: phase, answer: answer, from_options: fromOptions
        }, function (err) {
            if (err) { alert("Ошибка отправки: " + err.message); return; }
            $dialogPending.classList.add("hidden");
            loadDialog(stageID);
        });
    }

    function cancelDialog(stageID) {
        apiPost("/api/stages/" + encodeURIComponent(stageID) + "/dialog/cancel", null, function (err) {
            if (err) alert("Ошибка отмены: " + err.message);
        });
    }

    $dialogToggle.onclick = function () {
        $dialogHistory.classList.toggle("collapsed");
        $dialogToggle.textContent = $dialogHistory.classList.contains("collapsed")
            ? "▾ РАЗВЕРНУТЬ ИСТОРИЮ"
            : "▴ СВЕРНУТЬ ИСТОРИЮ";
    };

    // ---- Render stages list ----
    function renderStages() {
        if (!state) return;

        $stagesList.innerHTML = "";
        var ids = state.stage_order && state.stage_order.length > 0
            ? state.stage_order
            : Object.keys(state.stages).sort();

        for (var i = 0; i < ids.length; i++) {
            var id = ids[i];
            var st = state.stages[id];
            var li = document.createElement("li");
            li.className = "stage-item" + (id === selectedStageID ? " active" : "");
            li.dataset.stageId = id;

            var dot = document.createElement("span");
            dot.className = "status-dot";
            dot.dataset.status = st.status;

            var name = document.createElement("span");
            name.textContent = id;

            li.appendChild(dot);
            li.appendChild(name);

            if (st.status === "awaiting_user_input") {
                var badge = document.createElement("span");
                badge.className = "dialog-badge";
                badge.textContent = "💬";
                li.appendChild(badge);
            }

            li.addEventListener("click", onStageClick);
            $stagesList.appendChild(li);
        }
    }

    // ---- Render detail ----
    function renderDetail() {
        if (!selectedStageID) {
            $detailEmpty.classList.remove("hidden");
            $detailContent.classList.add("hidden");
            return;
        }

        $detailEmpty.classList.add("hidden");
        $detailContent.classList.remove("hidden");

        var st = state.stages[selectedStageID];
        if (!st) return;

        $detailTitle.textContent = selectedStageID;
        $detailStatus.textContent = statusLabels[st.status] || st.status;
        $detailStatus.dataset.status = st.status;

        // Clear comments when switching stages
        // (only if stage changed)
        // show/hide actions
        if (st.status === "awaiting_approval") {
            $actionsSection.classList.remove("hidden");
            $btnApprove.disabled = false;
            $commentHint.classList.remove("hidden");
        } else {
            $actionsSection.classList.add("hidden");
            $commentHint.classList.add("hidden");
            // Clear comments if stage is no longer awaiting_approval
            lineComments = {};
        }

        // Show retry button for failed stages
        if (st.status === "failed") {
            $retrySection.classList.remove("hidden");
        } else {
            $retrySection.classList.add("hidden");
        }

        // Start or stop log auto-refresh based on stage status
        startLogPolling();

        loadPlan();
        loadLog();
    }

    function loadPlan() {
        $planContent.innerHTML = "";
        $planEmpty.classList.add("hidden");
        $planContent.classList.add("hidden");

        apiGet("/api/stages/" + encodeURIComponent(selectedStageID) + "/plan", function (err, text) {
            if (err || !text || !text.trim()) {
                $planEmpty.classList.remove("hidden");
                return;
            }

            var st = state.stages[selectedStageID];
            // Mark all checkboxes as done when stage is completed
            if (st && (st.status === "done")) {
                text = text.replace(/- \[ \]/g, "- [x]");
            }
            // Use review mode only for awaiting_approval
            if (st && st.status === "awaiting_approval") {
                renderPlanReview(text);
            } else {
                // Regular markdown render for other statuses
                $planContent.classList.remove("hidden");
                renderMarkdownInto($planContent, text);
            }
        });
    }

    function loadLog() {
        $logEmpty.classList.add("hidden");
        $logContent.classList.add("hidden");

        apiGet("/api/stages/" + encodeURIComponent(selectedStageID) + "/log", function (err, text) {
            if (err || !text || !text.trim()) {
                $logContent.textContent = "";
                $logEmpty.classList.remove("hidden");
                return;
            }
            $logContent.classList.remove("hidden");
            var html = renderLog(text);
            if ($logContent.innerHTML !== html) {
                $logContent.innerHTML = html;
                $logContent.scrollTop = $logContent.scrollHeight;
            }
        });
    }

    // Log format: "HH:MM:SS  TYPE    detail"
    // Stage log shows only agent's text output (tool calls are in the event feed).
    function renderLog(text) {
        var lines = text.split("\n");
        var html = [];
        for (var i = 0; i < lines.length; i++) {
            var line = lines[i];
            // Skip non-text lines (tool calls, banners, etc.)
            if (!/^\d{2}:\d{2}:\d{2}\s+text\s/.test(line)) continue;
            html.push(escapeHTML(line));
        }
        return html.join("\n");
    }

    // ---- Log auto-polling ----
    function startLogPolling() {
        if (logPollTimer) {
            clearInterval(logPollTimer);
            logPollTimer = null;
        }

        if (!selectedStageID || !state) return;
        var st = state.stages[selectedStageID];
        if (st && (st.status === "planning" || st.status === "running" || st.status === "revising" || st.status === "retrying" || st.status === "awaiting_user_input")) {
            logPollTimer = setInterval(loadLog, 3000);
        }
    }

    // ---- Progress bar ----
    function renderProgress() {
        if (!state) return;

        var total = Object.keys(state.stages).length;
        var done = 0;
        var ids = Object.keys(state.stages);
        for (var i = 0; i < ids.length; i++) {
            if (state.stages[ids[i]].status === "done") done++;
        }

        var pct = total > 0 ? Math.round((done / total) * 100) : 0;
        $progressFill.style.width = pct + "%";
        $progressText.textContent = done + " / " + total;

        if (state.started_at) {
            $startedAt.textContent = formatTime(new Date(state.started_at));
        }
    }

    function formatTime(d) {
        var pad = function (n) { return n < 10 ? "0" + n : "" + n; };
        return pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
    }

    function formatDuration(ms) {
        var sec = Math.floor(ms / 1000);
        var h = Math.floor(sec / 3600);
        var m = Math.floor((sec % 3600) / 60);
        var s = sec % 60;
        var pad = function (n) { return n < 10 ? "0" + n : "" + n; };
        if (h > 0) return h + ":" + pad(m) + ":" + pad(s);
        return pad(m) + ":" + pad(s);
    }

    function formatRelativeTime(unixMs) {
        var diffSec = Math.max(0, Math.floor((Date.now() - unixMs) / 1000));
        if (diffSec < 60)     return diffSec + "s";
        if (diffSec < 3600)   return Math.floor(diffSec / 60) + "m";
        if (diffSec < 86400)  return Math.floor(diffSec / 3600) + "h";
        return Math.floor(diffSec / 86400) + "d";
    }

    function updateElapsed() {
        if (!state || !state.started_at) {
            $elapsed.textContent = "--";
            return;
        }
        var start = new Date(state.started_at).getTime();
        var now = Date.now();
        $elapsed.textContent = formatDuration(now - start);
    }

    // ---- Event handlers ----
    function onStageClick(e) {
        var li = e.currentTarget;
        selectedStageID = li.dataset.stageId;
        lineComments = {};
        activeCommentLine = null;
        dialogEntries = [];
        $dialogSection.classList.add("hidden");
        renderStages();
        renderDetail();
        loadDialog(selectedStageID);
    }

    function reloadAfterAction() {
        setTimeout(function () { loadState(); }, 300);
        setTimeout(function () { loadState(); }, 1500);
    }

    $btnApprove.addEventListener("click", function () {
        if (!selectedStageID) return;
        $btnApprove.disabled = true;
        apiPost("/api/stages/" + encodeURIComponent(selectedStageID) + "/approve", null, function (err) {
            if (err) {
                $btnApprove.disabled = false;
                console.error("approve error:", err);
            } else {
                reloadAfterAction();
            }
        });
    });

    $btnRevise.addEventListener("click", function () {
        if (!selectedStageID) return;
        var feedback = buildFeedbackString();
        if (!feedback) return;

        $btnRevise.disabled = true;
        apiPost("/api/stages/" + encodeURIComponent(selectedStageID) + "/revise", { feedback: feedback }, function (err) {
            $btnRevise.disabled = false;
            if (err) {
                console.error("revise error:", err);
                return;
            }
            lineComments = {};
            activeCommentLine = null;
            reloadAfterAction();
        });
    });

    $btnRetry.addEventListener("click", function () {
        if (!selectedStageID) return;
        $btnRetry.disabled = true;
        apiPost("/api/stages/" + encodeURIComponent(selectedStageID) + "/retry", null, function (err) {
            $btnRetry.disabled = false;
            if (err) {
                console.error("retry error:", err);
            } else {
                reloadAfterAction();
            }
        });
    });

    // ---- WebSocket ----
    function connectWS() {
        var proto = location.protocol === "https:" ? "wss:" : "ws:";
        var url = proto + "//" + location.host + "/ws";

        ws = new WebSocket(url);

        ws.onopen = function () {
            $wsStatus.textContent = "LINK";
            $wsStatus.className = "ws-status connected";
            reconnectDelay = 1000;
        };

        ws.onclose = function () {
            $wsStatus.textContent = "OFFLINE";
            $wsStatus.className = "ws-status disconnected";
            setTimeout(connectWS, reconnectDelay);
            reconnectDelay = Math.min(reconnectDelay * 2, 10000);
        };

        ws.onerror = function () {
            // onclose will fire after this
        };

        ws.onmessage = function (evt) {
            var ev;
            try {
                ev = JSON.parse(evt.data);
            } catch (e) {
                return;
            }
            handleEvent(ev);
        };
    }

    function addFeedEntry(ev) {
        var div = document.createElement("div");
        div.className = "feed-entry";

        var ts = Date.now();
        div.dataset.ts = ts;

        var stageID = ev.stage_id || "";
        var msg = "";
        var msgClass = "feed-msg";
        var statusClass = "";

        switch (ev.type) {
            case "stage_status_changed":
                var statusStr = (typeof ev.data === "string") ? ev.data : ((ev.data && ev.data.status) || "");
                msg = "\u2192 " + (statusStr || (typeof ev.data === "string" ? ev.data : ""));
                statusClass = statusStr ? "status-" + statusStr.replace(/[^a-z0-9_]/gi, "") : "";
                break;
            case "agent_completed":
                msg = "\u0430\u0433\u0435\u043d\u0442 " + (ev.data || "") + " \u0437\u0430\u0432\u0435\u0440\u0448\u0451\u043d";
                break;
            case "agent_action":
                var actionData = ev.data || {};
                var tool = actionData.tool || "";
                var detail = actionData.detail || "";
                msg = tool + (detail ? ": " + detail : "");
                msgClass = "feed-msg action";
                break;
            case "approved":
                msg = "\u043e\u0434\u043e\u0431\u0440\u0435\u043d\u043e";
                statusClass = "status-awaiting_approval";
                break;
            case "revised":
                msg = "\u043f\u0440\u0430\u0432\u043a\u0438: " + (ev.data || "");
                msgClass = "feed-msg error";
                statusClass = "status-revising";
                break;
            case "retry_scheduled":
                msg = "\u043f\u043e\u0432\u0442\u043e\u0440: " + (ev.data || "");
                statusClass = "status-retrying";
                break;
            case "retry_exhausted":
                msg = "\u043f\u043e\u043f\u044b\u0442\u043a\u0438 \u0438\u0441\u0447\u0435\u0440\u043f\u0430\u043d\u044b";
                statusClass = "status-failed";
                msgClass = "feed-msg error";
                break;
            case "manual_retry":
                msg = "\u0440\u0443\u0447\u043d\u043e\u0439 \u043f\u043e\u0432\u0442\u043e\u0440";
                statusClass = "status-retrying";
                break;
            case "ask_user":
                msg = "\u0432\u043e\u043f\u0440\u043e\u0441 \u0430\u0433\u0435\u043d\u0442\u0443";
                statusClass = "status-awaiting_user_input";
                break;
            case "user_answered":
                msg = "\u043e\u0442\u0432\u0435\u0442 \u043f\u043e\u043b\u044c\u0437\u043e\u0432\u0430\u0442\u0435\u043b\u044e";
                statusClass = "status-running";
                break;
            default:
                msg = ev.type;
        }

        var badgeHTML = stageID
            ? '<span class="feed-stage-badge ' + statusClass + '">' + escapeHTML(stageID) + "</span>"
            : "";

        div.innerHTML =
            '<span class="feed-time">' + formatRelativeTime(ts) + "</span>" +
            '<span class="' + msgClass + '">' + badgeHTML + escapeHTML(msg) + "</span>";

        $feedContent.appendChild(div);
        while ($feedContent.children.length > 200) {
            $feedContent.removeChild($feedContent.firstChild);
        }
        $feedContent.scrollTop = $feedContent.scrollHeight;
    }

    function handleEvent(ev) {
        addFeedEntry(ev);

        if (ev.type === "stage_status_changed") {
            if (state && state.stages && ev.stage_id) {
                var newStatus = (typeof ev.data === "string") ? ev.data : ((ev.data && ev.data.status) || "pending");
                if (state.stages[ev.stage_id]) {
                    state.stages[ev.stage_id].status = newStatus;
                    state.stages[ev.stage_id].updated_at = new Date().toISOString();
                } else {
                    state.stages[ev.stage_id] = {
                        status: newStatus,
                        updated_at: new Date().toISOString()
                    };
                }
            }
            renderStages();
            renderProgress();
            if (ev.stage_id === selectedStageID) {
                renderDetail();
            }
            return;
        }

        if (ev.type === "agent_action") {
            if (selectedStageID) {
                var st = state && state.stages[selectedStageID];
                if (st && (st.status === "planning" || st.status === "running" || st.status === "revising" || st.status === "retrying")) {
                    loadLog();
                }
            }
            return;
        }

        if (ev.type === "ask_user" || ev.type === "user_answered") {
            if (ev.stage_id === selectedStageID) {
                loadDialog(ev.stage_id);
            }
            loadState();
            return;
        }

        loadState();
    }

    // ---- Load initial state ----
    function loadState() {
        apiGet("/api/status", function (err, text) {
            if (err) {
                console.error("status load:", err);
                return;
            }
            try {
                state = JSON.parse(text);
            } catch (e) {
                console.error("status parse:", e);
                return;
            }
            $flowName.textContent = state.flow_name || "";
            renderStages();
            renderProgress();
            if (selectedStageID) {
                renderDetail();
            }
        });
    }

    // ---- Init ----
    loadState();
    connectWS();

    elapsedTimer = setInterval(updateElapsed, 1000);

    setInterval(function () {
        var nodes = $feedContent.querySelectorAll(".feed-entry");
        for (var i = 0; i < nodes.length; i++) {
            var ts = parseInt(nodes[i].dataset.ts, 10);
            if (!isNaN(ts)) {
                var t = nodes[i].querySelector(".feed-time");
                if (t) t.textContent = formatRelativeTime(ts);
            }
        }
    }, 5000);
})();
