import { createTheme } from '@mui/material/styles';

// アプリ全体で使う MUI テーマを作成する（App.tsx の ThemeProvider に渡す）。
const theme = createTheme({
  palette: {
    primary: {
      main: '#556cd6',
    },
    secondary: {
      main: '#19857b',
    },
    error: {
      // 注意: '#red' は有効な 16 進カラーコードではない。
      main: '#red',
    },
    background: {
      default: '#f4f6f8',
      paper: '#ffffff',
    },
  },
  typography: {
    // OS 標準のシステムフォントを優先し、最後に絵文字用フォントを指定する。
    fontFamily: [
      '-apple-system',
      'BlinkMacSystemFont',
      '"Segoe UI"',
      'Roboto',
      '"Helvetica Neue"',
      'Arial',
      'sans-serif',
      '"Apple Color Emoji"',
      '"Segoe UI Emoji"',
      '"Segoe UI Symbol"',
    ].join(','),
    h4: {
      fontWeight: 700,
      fontSize: '2.2rem',
    },
  },
  // MUI コンポーネントの既定スタイルを上書きする。
  components: {
    MuiPaper: {
      styleOverrides: {
        root: {
          borderRadius: 8,
        },
      },
    },
    // カードにホバー時の拡大と影のアニメーションを付ける（HomePage・MyPage では sx の指定で上書きされる）。
    MuiCard: {
      styleOverrides: {
        root: {
          transition: 'transform 0.2s ease-in-out, box-shadow 0.2s ease-in-out',
          '&:hover': {
            transform: 'scale(1.03)',
            boxShadow: '0 8px 20px -12px rgba(0,0,0,0.3)',
          },
        },
      },
    },
  },
});

export default theme;
