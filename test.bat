@echo off
setlocal enabledelayedexpansion

:: ============================================================================
::  test.bat - プロジェクトのテストを実行する
:: ============================================================================

:: 作業ディレクトリ（プロジェクトルート）の取得
set "PROJECT_ROOT=%CD%"

:: 安全チェック：危険なシステムディレクトリでは実行しない
if /i "%PROJECT_ROOT%"=="C:\" (
    echo [ERROR] SAFETY GUARD: Running directly on C:\ is prohibited!
    exit /b 1
)
if /i "%PROJECT_ROOT%"=="C:\Windows" (
    echo [ERROR] SAFETY GUARD: Running in system directory is prohibited!
    exit /b 1
)

echo [INFO] Running tests in: %PROJECT_ROOT%
echo.

:: Go がインストールされているか確認
where go >nul 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] Go is not installed or not in PATH.
    echo Please install Go from https://go.dev/dl/
    exit /b 1
)

:: プロジェクト内の go.mod があるか確認
if not exist "go.mod" (
    echo [ERROR] go.mod not found in %PROJECT_ROOT%
    echo This does not appear to be a Go project root.
    exit /b 1
)

:: ビルドとテストを実行
echo [INFO] Running go test ...
go test ./...
set "TEST_RESULT=%errorlevel%"

echo.
if %TEST_RESULT% equ 0 (
    echo [OK] All tests passed.
) else (
    echo [FAIL] Some tests failed (exit code %TEST_RESULT%).
)

exit /b %TEST_RESULT%
