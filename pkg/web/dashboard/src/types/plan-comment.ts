// Комментарий к строке плана: создаётся в режиме ревью (awaiting_approval),
// отображается в панели плана, входит в feedback ревизии.
export type PlanComment = {
  line: number
  text: string
}
