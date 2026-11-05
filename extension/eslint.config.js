import {defineConfig} from 'eslint/config';
import gts from 'gts';
import tseslint from 'typescript-eslint';

const noElse = {
  selector: 'IfStatement > .alternate',
  message:
    'No else or else if: return early from the if branch (early-return rule).',
};

export default defineConfig([
  {ignores: ['dev/', 'node_modules/', 'agents/']},
  ...gts,
  {
    files: ['eslint.config.js'],
    languageOptions: {sourceType: 'module'},
  },
  {
    files: ['**/*.ts'],
    extends: [
      tseslint.configs.strictTypeChecked,
      tseslint.configs.stylisticTypeChecked,
    ],
    languageOptions: {
      parserOptions: {
        project: './tsconfig.json',
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },
  {
    rules: {
      'max-depth': ['error', 3],
      'no-restricted-syntax': ['error', noElse],
      'no-else-return': 'error',
      'no-nested-ternary': 'error',
      'no-param-reassign': 'error',
      'prefer-const': 'error',
      complexity: ['error', 10],
    },
  },
  {
    files: ['index.ts', 'src/**/*.ts'],
    ignores: ['src/**/*.spec.ts'],
    rules: {
      'max-lines': [
        'error',
        {max: 300, skipBlankLines: true, skipComments: true},
      ],
    },
  },
]);
