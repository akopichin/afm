// afm Dashboard — client-side logic.
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
    var $feedPanel = document.getElementById("feed-panel");
    var $retrySection = document.getElementById("retry-section");
    var $btnRetry = document.getElementById("btn-retry");
    var $dialogSection = document.getElementById("dialog-section");
    var $dialogHistory = document.getElementById("dialog-history");
    var $dialogPending = document.getElementById("dialog-pending");
    var $dialogToggle = document.getElementById("dialog-toggle");

    // ---- Consumption panel refs ----
    var $usagePanel = document.getElementById("usage-panel");
    var $usageToggle = document.getElementById("usage-toggle");
    var $usageMetricSwitch = document.getElementById("usage-metric-switch");
    var $usageStageSelect = document.getElementById("usage-stage-select");
    var $usageChart = document.getElementById("usage-chart");
    var $usageEmpty = document.getElementById("usage-empty");
    var $usageTotal = document.getElementById("usage-total");
    var $usageMeta = document.getElementById("usage-meta");

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
    var lastFlashedQuestionID = null;

    // ---- Consumption panel state ----
    // currentUsageMetric по умолчанию "tokens"; "cost" рендерится только если пробный
    // запрос /api/usage?metric=cost вернул непустой массив (pricing не настроен → []).
    var currentUsageMetric = "tokens";
    var currentUsageStage = "";     // "" = все стадии
    var usageInited = false;
    var usageStageSignature = null; // чтобы не перестраивать <select> на каждый апдейт
    var usageRefreshTimer = null;

    // Палитра графика — читается из CSS-токенов активной темы, с fallback на
    // mint-палитру Nova Corps. var() в SVG presentation attributes не работает,
    // а генерить SVG удобнее строками — поэтому читаем computed values один раз.
    // В Nova Corps --usage-grid не определён → fallback → график не меняется;
    // в goga-теме --mint/--amber/--ink-dim/--usage-grid дают teal/blue палитру.
    var _cssVars = getComputedStyle(document.documentElement);
    function _cssVar(name, fallback) {
        var v = _cssVars.getPropertyValue(name).trim();
        return v || fallback;
    }
    var USAGE_COLORS = {
        mint:   _cssVar("--mint",       "#6fd4cc"),
        amber:  _cssVar("--amber",      "#e5d442"),
        inkDim: _cssVar("--ink-dim",    "#4a8a85"),
        grid:   _cssVar("--usage-grid", "rgba(111, 212, 204, 0.10)")
    };

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

    // ---- Status labels ----
    var statusLabels = {
        pending: "Pending",
        planning: "Planning",
        awaiting_approval: "Awaiting approval",
        revising: "Revising",
        ready: "Ready",
        running: "Running",
        done: "Done",
        failed: "Failed",
        retrying: "Retrying",
        awaiting_user_input: "Awaiting reply"
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
        textarea.placeholder = "Comment on line " + lineNum + "...";
        textarea.value = lineComments[lineNum] || "";

        var actions = document.createElement("div");
        actions.className = "comment-actions";

        var btnAdd = document.createElement("button");
        btnAdd.className = "btn btn-send";
        btnAdd.textContent = lineComments[lineNum] ? "Update" : "Add";
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
        btnCancel.textContent = "Cancel";
        btnCancel.addEventListener("click", function (ev) {
            ev.stopPropagation();
            closeCommentForm();
            activeCommentLine = null;
        });

        var btnDelete = document.createElement("button");
        btnDelete.className = "btn btn-cancel";
        btnDelete.textContent = "Delete";
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
        display.innerHTML = '<div style="color:var(--c-awaiting);font-size:12px;margin-bottom:4px">Comment on line ' + lineNum + '</div>' +
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
        $btnRevise.textContent = count > 0 ? "Send revision (" + count + ")" : "Send revision";
    }

    function buildFeedbackString() {
        var parts = [];
        var lineNums = Object.keys(lineComments).map(Number).sort(function (a, b) { return a - b; });
        for (var i = 0; i < lineNums.length; i++) {
            var n = lineNums[i];
            parts.push("Line " + n + ": " + lineComments[n]);
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
            var questionKey = (open.phase || "") + "." + (open.id || "");
            if (questionKey !== lastFlashedQuestionID) {
                lastFlashedQuestionID = questionKey;
                $dialogSection.classList.remove("dialog-flash");
                void $dialogSection.offsetWidth;
                $dialogSection.classList.add("dialog-flash");
                setTimeout(function () {
                    $dialogSection.scrollIntoView({ behavior: "smooth", block: "nearest" });
                }, 50);
            }
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
            if (!confirm("Cancel stage?")) return;
            cancelDialog(stageID);
        };
    }

    function sendAnswer(stageID, qID, phase, answer, fromOptions) {
        apiPost("/api/stages/" + encodeURIComponent(stageID) + "/dialog/answer", {
            id: qID, phase: phase, answer: answer, from_options: fromOptions
        }, function (err) {
            if (err) { alert("Send error: " + err.message); return; }
            $dialogPending.classList.add("hidden");
            loadDialog(stageID);
        });
    }

    function cancelDialog(stageID) {
        apiPost("/api/stages/" + encodeURIComponent(stageID) + "/dialog/cancel", null, function (err) {
            if (err) alert("Cancel error: " + err.message);
        });
    }

    $dialogToggle.onclick = function () {
        $dialogHistory.classList.toggle("collapsed");
        $dialogToggle.textContent = $dialogHistory.classList.contains("collapsed")
            ? "▾ EXPAND HISTORY"
            : "▴ COLLAPSE HISTORY";
    };

    // ---- Consumption panel ----
    // Слайд-аут панель потребления: свич метрик (tokens/cost/kb), фильтр по стейджам
    // (из уже загруженного списка state), hand-rolled SVG-график временного ряда по
    // []UsageAggregate, сгруппированный по timeBucket. Источник: GET /api/usage.

    // Форматирование значения под метрику (итог и тултипы точек).
    function formatUsageValue(v, metric) {
        if (metric === "cost") return formatUsageCost(v);
        if (metric === "kb") return v.toFixed(1) + " KB";
        return Math.round(v).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ",") + " tok";
    }

    // Стоимость: маленькие суммы показываем с большей точностью (cost обычно < $0.01).
    function formatUsageCost(v) {
        if (v === 0) return "$0";
        if (v < 0.01) return "$" + v.toFixed(4);
        return "$" + v.toFixed(2);
    }

    // Компактная подпись для оси Y (помещается в узкую панель).
    function formatUsageAxis(v, metric) {
        if (v === 0) return "0";
        if (metric === "cost") return v < 0.01 ? v.toFixed(3) : v.toFixed(2);
        if (metric === "kb") return Math.round(v).toString();
        if (v >= 1000) return (v / 1000).toFixed(v >= 10000 ? 0 : 1) + "k";
        return Math.round(v).toString();
    }

    // "2026-07-07T10:05:00Z" -> "10:05" (подпись точки по оси X).
    function formatUsageBucket(rfc3339) {
        var m = /T(\d{2}:\d{2})/.exec(rfc3339 || "");
        return m ? m[1] : (rfc3339 || "");
    }

    function usageSum(aggregates) {
        var s = 0;
        for (var i = 0; i < aggregates.length; i++) s += aggregates[i].value || 0;
        return s;
    }

    // Рисует SVG-график: оси, сетка, area+line, точки с тултипами, подписи бакетов.
    // Пустой массив → состояние «Нет данных».
    function renderUsageChart(aggregates) {
        var metric = currentUsageMetric;
        if (!aggregates || aggregates.length === 0) {
            $usageChart.innerHTML = "";
            $usageEmpty.classList.remove("hidden");
            return;
        }
        $usageEmpty.classList.add("hidden");

        // Сортируем по timeBucket (RFC3339 лексикографически сравним для UTC).
        var pts = aggregates.slice().sort(function (a, b) {
            return a.timeBucket < b.timeBucket ? -1 : a.timeBucket > b.timeBucket ? 1 : 0;
        });

        var W = 320, H = 180;
        var padL = 38, padR = 10, padT = 12, padB = 24;
        var plotW = W - padL - padR;
        var plotH = H - padT - padB;

        var max = 0;
        for (var i = 0; i < pts.length; i++) if (pts[i].value > max) max = pts[i].value;
        if (max <= 0) max = 1;

        var n = pts.length;
        function xAt(idx) {
            return n <= 1 ? padL + plotW / 2 : padL + (plotW * idx) / (n - 1);
        }
        function yAt(v) { return padT + plotH * (1 - v / max); }

        // Горизонтальная сетка + подписи оси Y (4 деления).
        var ticks = 4;
        var gridSvg = "";
        for (var g = 0; g <= ticks; g++) {
            var gv = (max * g) / ticks;
            var gy = yAt(gv);
            gridSvg += '<line x1="' + padL + '" y1="' + gy.toFixed(1) + '" x2="' + (W - padR) + '" y2="' + gy.toFixed(1) + '" stroke="' + USAGE_COLORS.grid + '" stroke-width="1"/>';
            gridSvg += '<text x="' + (padL - 6) + '" y="' + (gy + 3).toFixed(1) + '" text-anchor="end" fill="' + USAGE_COLORS.inkDim + '" font-size="8" font-family="inherit">' + formatUsageAxis(gv, metric) + '</text>';
        }

        // Линия + заливка под ней.
        var linePts = [];
        for (var k = 0; k < n; k++) {
            linePts.push(xAt(k).toFixed(1) + "," + yAt(pts[k].value).toFixed(1));
        }
        var baseY = (padT + plotH).toFixed(1);
        var lineD = "M " + linePts.join(" L ");
        var areaD = "M " + padL + "," + baseY + " L " + linePts.join(" L ") +
            " L " + xAt(n - 1).toFixed(1) + "," + baseY + " Z";

        // Точки + подписи оси X (прореживаем, чтобы не налезали).
        var ptsSvg = "";
        var labelStep = Math.max(1, Math.ceil(n / 6));
        for (var p = 0; p < n; p++) {
            var px = xAt(p), py = yAt(pts[p].value);
            ptsSvg += '<circle cx="' + px.toFixed(1) + '" cy="' + py.toFixed(1) + '" r="2.4" fill="' + USAGE_COLORS.amber + '"><title>' +
                escapeHTML(formatUsageBucket(pts[p].timeBucket)) + " · " +
                escapeHTML(formatUsageValue(pts[p].value, metric)) + "</title></circle>";
            if (p % labelStep === 0 || p === n - 1) {
                ptsSvg += '<text x="' + px.toFixed(1) + '" y="' + (H - 8) + '" text-anchor="middle" fill="' + USAGE_COLORS.inkDim + '" font-size="8" font-family="inherit">' + formatUsageBucket(pts[p].timeBucket) + "</text>";
            }
        }

        $usageChart.innerHTML =
            '<defs><linearGradient id="usageAreaGrad" x1="0" y1="0" x2="0" y2="1">' +
              '<stop offset="0%" stop-color="' + USAGE_COLORS.mint + '" stop-opacity="0.35"/>' +
              '<stop offset="100%" stop-color="' + USAGE_COLORS.mint + '" stop-opacity="0.02"/>' +
            "</linearGradient></defs>" +
            gridSvg +
            '<path d="' + areaD + '" fill="url(#usageAreaGrad)" stroke="none"/>' +
            '<path d="' + lineD + '" fill="none" stroke="' + USAGE_COLORS.mint + '" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round"/>' +
            ptsSvg;
    }

    // Загружает агрегаты по текущим metric/stage и перерисовывает график + итог.
    function loadUsage() {
        var metric = currentUsageMetric;
        var stage = currentUsageStage;
        apiGet("/api/usage?metric=" + encodeURIComponent(metric) + "&stage=" + encodeURIComponent(stage), function (err, text) {
            if (err) {
                renderUsageChart([]);
                $usageTotal.textContent = "—";
                $usageMeta.textContent = "request error";
                return;
            }
            var data;
            try { data = JSON.parse(text); } catch (_) { data = null; }
            if (!Array.isArray(data)) data = [];
            renderUsageChart(data);
            $usageTotal.textContent = formatUsageValue(usageSum(data), metric);
            $usageMeta.innerHTML = '<span>' + data.length + " points</span>" +
                '<span>' + (stage ? escapeHTML(stage) : "all stages") + "</span>";
        });
    }

    // Один пробный запрос cost: pricing не настроен → сервер вернёт 200 [] → опцию
    // «Стоимость» прячем на клиенте (не показываем тоггл, который всегда пустой).
    function probeUsageCost(cb) {
        apiGet("/api/usage?metric=cost&stage=", function (err, text) {
            if (err) { cb(false); return; }
            var data;
            try { data = JSON.parse(text); } catch (_) { data = null; }
            cb(Array.isArray(data) && data.length > 0);
        });
    }

    // Перестраивает <select> стейджей, если состав списка изменился (не на каждый
    // апдейт — чтобы не сбрасывать открытый пользователем dropdown). Источник — тот
    // же state.stage_order/stages, что и у списка стадий, без нового API-вызова.
    function refreshUsageStageOptions() {
        if (!state) return;
        var ids = state.stage_order && state.stage_order.length > 0
            ? state.stage_order
            : Object.keys(state.stages).sort();
        var sig = ids.join(",");
        if (sig === usageStageSignature) {
            $usageStageSelect.value = currentUsageStage || "";
            return;
        }
        usageStageSignature = sig;

        var prev = currentUsageStage || "";
        var html = '<option value="">All stages</option>';
        for (var i = 0; i < ids.length; i++) {
            var id = ids[i];
            html += '<option value="' + escapeHTML(id) + '"' +
                (id === prev ? " selected" : "") + ">" + escapeHTML(id) + "</option>";
        }
        $usageStageSelect.innerHTML = html;
        // Ранее выбранный стейдж исчез из рана — сбрасываем на «все».
        if (prev && ids.indexOf(prev) === -1) {
            currentUsageStage = "";
        }
        $usageStageSelect.value = currentUsageStage || "";
    }

    function initUsagePanel() {
        usageInited = true;

        var metricBtns = $usageMetricSwitch.querySelectorAll(".usage-metric");
        metricBtns.forEach(function (b) {
            b.addEventListener("click", function () {
                var m = b.getAttribute("data-metric");
                if (!m || m === currentUsageMetric) return;
                currentUsageMetric = m;
                metricBtns.forEach(function (x) { x.classList.toggle("active", x === b); });
                loadUsage();
            });
        });

        $usageStageSelect.addEventListener("change", function () {
            currentUsageStage = $usageStageSelect.value;
            loadUsage();
        });

        // Прячем/показываем «Стоимость» по результату пробного cost-запроса.
        probeUsageCost(function (available) {
            var costBtn = $usageMetricSwitch.querySelector('[data-metric="cost"]');
            if (costBtn) costBtn.classList.toggle("hidden", !available);
            // Если cost был активен, а оказался недоступен — откатываемся на tokens.
            if (!available && currentUsageMetric === "cost") {
                currentUsageMetric = "tokens";
                var tok = $usageMetricSwitch.querySelector('[data-metric="tokens"]');
                metricBtns.forEach(function (x) { x.classList.toggle("active", x === tok); });
                loadUsage();
            }
        });
    }

    function openUsagePanel() {
        $usagePanel.classList.add("open");
        // aria-hidden на тело (не на aside) — вкладка-тоггл остаётся доступной всегда.
        $usagePanel.querySelector(".usage-panel-body").setAttribute("aria-hidden", "false");
        if (!usageInited) initUsagePanel();
        refreshUsageStageOptions();
        loadUsage();
        // Живой апдейт, пока панель открыта (как поллинг лога/событий в остальном UI).
        if (!usageRefreshTimer) usageRefreshTimer = setInterval(usageRefreshTick, 10000);
    }

    function closeUsagePanel() {
        $usagePanel.classList.remove("open");
        $usagePanel.querySelector(".usage-panel-body").setAttribute("aria-hidden", "true");
        if (usageRefreshTimer) {
            clearInterval(usageRefreshTimer);
            usageRefreshTimer = null;
        }
    }

    function usageRefreshTick() {
        if ($usagePanel.classList.contains("open")) loadUsage();
    }

    $usageToggle.addEventListener("click", function () {
        if ($usagePanel.classList.contains("open")) closeUsagePanel();
        else openUsagePanel();
    });

    // ---- Stage focus helpers ----
    var ACTIVE_STATUSES = { running: 1, planning: 1, revising: 1, retrying: 1, awaiting_user_input: 1 };

    function findActiveStage(startAfterID) {
        if (!state) return null;
        var ids = state.stage_order && state.stage_order.length > 0
            ? state.stage_order
            : Object.keys(state.stages).sort();
        var startIdx = startAfterID ? ids.indexOf(startAfterID) + 1 : 0;
        if (startIdx < 0) startIdx = 0;
        for (var i = startIdx; i < ids.length; i++) {
            if (state.stages[ids[i]] && ACTIVE_STATUSES[state.stages[ids[i]].status]) return ids[i];
        }
        return null;
    }

    function findFirstStage(status) {
        if (!state) return null;
        var ids = state.stage_order && state.stage_order.length > 0
            ? state.stage_order
            : Object.keys(state.stages).sort();
        for (var i = 0; i < ids.length; i++) {
            if (state.stages[ids[i]] && state.stages[ids[i]].status === status) return ids[i];
        }
        return null;
    }

    function selectStage(id) {
        selectedStageID = id;
        lineComments = {};
        activeCommentLine = null;
        dialogEntries = [];
        $dialogSection.classList.add("hidden");
        renderStages();
        renderDetail();
        loadDialog(id);
        var li = $stagesList.querySelector('[data-stage-id="' + id + '"]');
        if (li) li.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }

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

            var label = document.createElement("span");
            label.className = "stage-label";

            var idSpan = document.createElement("span");
            idSpan.className = "stage-id";
            idSpan.textContent = id;
            label.appendChild(idSpan);

            var stageName = state.stage_names && state.stage_names[id];
            if (stageName) {
                var nameSpan = document.createElement("span");
                nameSpan.className = "stage-name";
                nameSpan.textContent = stageName;
                label.appendChild(nameSpan);
            }

            li.appendChild(dot);
            li.appendChild(label);

            if (st.status === "awaiting_user_input") {
                var badge = document.createElement("span");
                badge.className = "dialog-badge";
                badge.textContent = "💬";
                li.appendChild(badge);
            }

            li.addEventListener("click", onStageClick);
            $stagesList.appendChild(li);
        }

        // Держим фильтр стейджей панели потребления в синхронизации со списком.
        refreshUsageStageOptions();
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

        $detailTitle.textContent = (state.stage_names && state.stage_names[selectedStageID]) || selectedStageID;
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
                msg = "agent " + (ev.data || "") + " completed";
                break;
            case "agent_action":
                var actionData = ev.data || {};
                var tool = actionData.tool || "";
                var detail = actionData.detail || "";
                msg = tool + (detail ? ": " + detail : "");
                msgClass = "feed-msg action";
                break;
            case "approved":
                msg = "approved";
                statusClass = "status-awaiting_approval";
                break;
            case "revised":
                msg = "revisions: " + (ev.data || "");
                msgClass = "feed-msg error";
                statusClass = "status-revising";
                break;
            case "retry_scheduled":
                msg = "retry: " + (ev.data || "");
                statusClass = "status-retrying";
                break;
            case "retry_exhausted":
                msg = "retries exhausted";
                statusClass = "status-failed";
                msgClass = "feed-msg error";
                break;
            case "manual_retry":
                msg = "manual retry";
                statusClass = "status-retrying";
                break;
            case "ask_user":
                msg = "question to agent";
                statusClass = "status-awaiting_user_input";
                break;
            case "user_answered":
                msg = "reply to user";
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
        $feedPanel.scrollTop = $feedPanel.scrollHeight;
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
                if (newStatus === "done") {
                    var nextID = findActiveStage(ev.stage_id);
                    if (nextID) selectStage(nextID);
                }
            } else if (ACTIVE_STATUSES[newStatus]) {
                if (!selectedStageID) {
                    selectStage(ev.stage_id);
                } else if (state.stages[selectedStageID] && state.stages[selectedStageID].status === "done") {
                    selectStage(ev.stage_id);
                }
            } else if (newStatus === "failed" && !selectedStageID) {
                selectStage(ev.stage_id);
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
            } else {
                var activeID = findActiveStage(null) || findFirstStage("failed");
                if (activeID) selectStage(activeID);
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
