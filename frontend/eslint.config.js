import js from "@eslint/js";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import globals from "globals";
import tseslint from "typescript-eslint";

/**
 * Flat config shared by the shared package and both applications.
 *
 * ESLint resolves this file upwards, so the workspaces do not each need their
 * own copy and cannot drift apart.
 */
export default tseslint.config(
  {
    ignores: ["**/dist/**", "**/node_modules/**", "**/coverage/**"],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      ecmaVersion: 2023,
      globals: {
        ...globals.browser,
      },
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],

      // docs/13-I18N-SPEC.md: every user-visible string must come from i18n, in
      // both locales. Enforcing it mechanically is the only way the rule
      // survives contact with a large codebase, so literal JSX text containing
      // any letter or CJK character is an error.
      //
      // Only JSX text is restricted: attribute values such as className, and
      // string literals in general, are not user-visible and must stay free so
      // that internal identifiers, status keys and contract codes read as
      // English literals (docs/13 requires internal keys to be English).
      "no-restricted-syntax": [
        "error",
        {
          selector: "JSXText[value=/[A-Za-z\\u3400-\\u9FFF]/]",
          message:
            "Hardcoded user-visible text. Move it into the i18n resources and render it with t(...) (docs/13).",
        },
      ],
    },
  },
  {
    // Test files may contain literal expectations.
    files: ["**/*.test.{ts,tsx}", "**/*.spec.{ts,tsx}"],
    rules: {
      "no-restricted-syntax": "off",
    },
  },
);
