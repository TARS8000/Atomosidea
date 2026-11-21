// ESLint 8 (legacy eslintrc) configuration — based on the Vite react-ts template.
module.exports = {
  root: true,
  env: { browser: true, es2020: true },
  extends: [
    'eslint:recommended',
    'plugin:@typescript-eslint/recommended',
    'plugin:react-hooks/recommended',
  ],
  ignorePatterns: ['dist', 'node_modules', '.eslintrc.cjs'],
  parser: '@typescript-eslint/parser',
  plugins: ['react-refresh'],
  rules: {
    'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
    // 既存コードの catch (err: any) 等が多数あるため、現状は無効化している
    '@typescript-eslint/no-explicit-any': 'off',
  },
};
