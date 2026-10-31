// plural — «1 model», «3 models»: к существительному добавляется s, кроме единственного числа.
export function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? '' : 's'}`
}
