package main

import (
	"fmt"
	"strings"
)

func questionIntent(c QuestionCard, kind IntentKind) Intent {
	return Intent{Kind: kind, RunKey: c.RunKey, StageID: c.StageID, QuestionID: c.QuestionID, QuestionPhase: c.QuestionPhase, QuestionDigest: c.QuestionDigest, StageUpdatedAt: c.StageUpdatedAt}
}

func RenderQuestion(c QuestionCard) View {
	lines := []string{"Вопрос · " + runName(c.RunLabel, c.RunKey)}
	if c.StageName != "" {
		lines = append(lines, "Стадия: "+c.StageName)
	}
	if c.Prompt != "" {
		lines = append(lines, c.Prompt)
	} else if c.State == DataEmpty || c.State == DataReady {
		lines = append(lines, "Текущего вопроса нет")
	}
	if c.Answered {
		lines = append(lines, "Ответ уже принят")
	}
	if s := stateText(c.State, c.Error); s != "" {
		lines = append(lines, s)
	}
	var rows [][]Button
	canAnswer := c.State == DataReady && c.CanAnswer && !c.Answered && c.QuestionID != "" && c.QuestionDigest != ""
	for i, opt := range c.Options {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, opt.Label))
		if opt.Value != opt.Label {
			lines = append(lines, "Значение: "+opt.Value)
		}
		if canAnswer {
			in := questionIntent(c, IntentAnswerQuestion)
			in.OptionValue = opt.Value
			rows = append(rows, []Button{btn(fmt.Sprintf("%d. Выбрать", i+1), in)})
		}
	}
	if canAnswer {
		if c.AllowCustom {
			rows = append(rows, []Button{btn("Свой ответ", questionIntent(c, IntentCustomAnswer))})
		}
	}
	if c.RunKey != "" {
		rows = append(rows, []Button{btn("Обновить вопрос", questionIntent(c, IntentRefresh))})
	}
	return onePart(strings.Join(lines, "\n"), rows...)
}

func planIntent(c PlanCard, kind IntentKind) Intent {
	return Intent{Kind: kind, RunKey: c.RunKey, StageID: c.StageID, PlanDigest: c.PlanDigest, ExpectedStatus: c.ExpectedStatus, StageUpdatedAt: c.StageUpdatedAt}
}

// Split by UTF-16 units, which also bounds Telegram's visible character count.
// The reserved header space keeps numbering below the message limit for every part.
func planChunks(source string) []string {
	const bodyLimit = telegramTextLimit - 256
	if source == "" {
		return []string{""}
	}
	var chunks []string
	for source != "" {
		part, rest := textPrefix(source, bodyLimit)
		chunks = append(chunks, part)
		source = rest
	}
	return chunks
}

func planStageControls(c PlanCard) [][]Button {
	var rows [][]Button
	if c.CanPause {
		rows = append(rows, []Button{btn("Приостановить стадию", planIntent(c, IntentPause))})
	}
	if c.CanContinue {
		rows = append(rows, []Button{btn("Продолжить стадию", planIntent(c, IntentContinue))})
	}
	return rows
}

func RenderPlan(c PlanCard) View {
	stage := c.StageName
	if stage == "" {
		stage = c.StageID
	}
	if stage == "" {
		stage = "стадия"
	}
	stage = shortText(stage, 120)
	if c.Plan == "" || c.State == DataLoading || c.State == DataError {
		text := "План · " + stage + " · " + runName(c.RunLabel, c.RunKey)
		if c.Plan == "" {
			text += "\nПлан пока недоступен"
		}
		if s := stateText(c.State, c.Error); s != "" {
			text += "\n" + s
		}
		rows := [][]Button{{btn("Обновить план", planIntent(c, IntentPlan))}}
		if c.State == DataReady {
			rows = append(rows, planStageControls(c)...)
		}
		return onePart(text, rows...)
	}
	chunks := planChunks(c.Plan)
	parts := make([]ViewPart, len(chunks))
	for i, chunk := range chunks {
		header := fmt.Sprintf("План · %s · %s\nЧасть %d/%d\n\n", stage, shortText(runName(c.RunLabel, c.RunKey), 80), i+1, len(chunks))
		parts[i] = ViewPart{Text: header + chunk}
	}
	if c.RevisionDraft != "" {
		draftChunks := planChunks(c.RevisionDraft)
		draftParts := make([]ViewPart, 0, len(draftChunks)+len(parts))
		for i, chunk := range draftChunks {
			draftParts = append(draftParts, ViewPart{Text: fmt.Sprintf("Черновик правки · часть %d/%d\n\n%s", i+1, len(draftChunks), chunk)})
		}
		parts = append(draftParts, parts...)
	}
	last := &parts[len(parts)-1]
	if c.State == DataOffline {
		// Keep source bytes intact: offline notice belongs in button labels or a
		// separate part, never inside a numbered source chunk.
		last.Buttons = append(last.Buttons, []Button{btn("Нет связи · только чтение", planIntent(c, IntentPlan))})
		return View{Parts: parts}
	}
	if c.CanApprove && c.PlanDigest != "" && !c.RevisionNeedsConfirmation {
		last.Buttons = append(last.Buttons, []Button{btn("Одобрить план", planIntent(c, IntentApprovePlan))})
	}
	if c.CanRevise {
		if c.RevisionNeedsConfirmation && c.RevisionDraft != "" {
			last.Buttons = append(last.Buttons, []Button{btn("Подтвердить правку", planIntent(c, IntentConfirmRevision)), btn("Отмена", Intent{Kind: IntentCancel, RunKey: c.RunKey})})
		} else {
			last.Buttons = append(last.Buttons, []Button{btn("Попросить правки", planIntent(c, IntentRevisePlan))})
		}
	}
	last.Buttons = append(last.Buttons, planStageControls(c)...)
	return View{Parts: parts}
}

func addAgentIntent(c AddAgentCard, kind IntentKind) Intent {
	mode := c.Mode
	if c.Scope == "run" {
		mode = ""
	}
	return Intent{Kind: kind, RunKey: c.RunKey, StageID: c.StageID, ProfileID: c.ProfileID, Scope: c.Scope, Mode: mode}
}

func RenderAddAgent(c AddAgentCard) View {
	lines := []string{"Добавить агента · " + runName(c.RunLabel, c.RunKey)}
	if c.StageName != "" {
		lines = append(lines, "Стадия: "+c.StageName)
	}
	selectedAvailable := false
	var rows [][]Button
	if len(c.Profiles) == 0 {
		if c.State == DataReady || c.State == DataEmpty {
			lines = append(lines, "Доступных профилей нет")
		}
	}
	for _, profile := range c.Profiles {
		name := profile.Name
		if name == "" {
			name = profile.ID
		}
		if profile.ID == c.ProfileID && profile.Available {
			selectedAvailable = true
		}
		if !profile.Available {
			lines = append(lines, name+" · недоступен: "+profile.Reason)
			continue
		}
		if c.State == DataReady {
			in := addAgentIntent(c, IntentSelectProfile)
			in.ProfileID = profile.ID
			label := "○ " + name
			if profile.ID == c.ProfileID {
				label = "✓ " + name
			}
			rows = append(rows, []Button{btn(shortText(label, 64), in)})
		}
	}
	scope := "не выбран"
	if c.Scope == "run" {
		scope = "весь запуск"
	} else if c.Scope == "stage" {
		scope = "стадия"
	}
	lines = append(lines, "Область: "+scope)
	if c.Scope == "stage" {
		mode := "совещательный"
		if c.Mode == "required" {
			mode = "обязательный"
		}
		lines = append(lines, "Режим помощника: "+mode)
	}
	if c.Instruction != "" {
		lines = append(lines, "Задача: "+c.Instruction)
	}
	if c.FirstMessage != "" {
		lines = append(lines, "Первое сообщение после создания: "+c.FirstMessage)
	}
	if s := stateText(c.State, c.Error); s != "" {
		lines = append(lines, s)
	}
	if c.State == DataReady {
		run := addAgentIntent(c, IntentSelectScope)
		run.Scope = "run"
		run.Mode = ""
		stage := addAgentIntent(c, IntentSelectScope)
		stage.Scope = "stage"
		rows = append(rows, []Button{btn("Агент запуска", run), btn("Помощник стадии", stage)})
		if c.Scope == "stage" {
			advisory := addAgentIntent(c, IntentSelectMode)
			advisory.Mode = "advisory"
			required := addAgentIntent(c, IntentSelectMode)
			required.Mode = "required"
			rows = append(rows, []Button{btn("Совещательный", advisory), btn("Обязательный", required)})
		}
		if c.CanAdd && selectedAvailable && c.Instruction != "" && (c.Scope == "run" || c.Scope == "stage" && c.StageID != "" && (c.Mode == "advisory" || c.Mode == "required")) {
			confirm := addAgentIntent(c, IntentConfirmAddAgent)
			confirm.ComposerDigest = composerDigest(c.Instruction, c.FirstMessage)
			rows = append(rows, []Button{btn("Добавить агента", confirm)})
		}
	}
	return onePart(strings.Join(lines, "\n"), rows...)
}
