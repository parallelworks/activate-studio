/** A count and its noun, singular for one: "1 file", "2 files". */
export const plural = (n: number, one: string, many = `${one}s`): string => `${n.toLocaleString()} ${n === 1 ? one : many}`
