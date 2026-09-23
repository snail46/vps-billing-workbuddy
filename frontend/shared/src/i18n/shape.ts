/**
 * Resource typing helpers.
 *
 * Locale resources are declared with `as const` so that key paths can be
 * derived, but that also freezes each value to its exact literal type. Typing a
 * second locale as `typeof first` would therefore demand the *same strings*, not
 * merely the same keys.
 *
 * `TranslationShape` keeps the structure and widens the leaves, which is exactly
 * the compile-time guarantee that matters: every key present in one locale must
 * be present, with the same nesting, in the other. A runtime test
 * (`locales.test.ts`) checks the same property in the other direction, so a key
 * added to one locale alone fails twice.
 */
export type TranslationShape<T> = {
  [K in keyof T]: T[K] extends string ? string : TranslationShape<T[K]>;
};
