import axios from 'axios';

/**
 * 分割アップロード（backend/shared/chunkupload）。
 *
 * Cloudflare の無料プランは 1 リクエスト 100 MB までなので、大きなファイルはサーバーが決めた大きさ（50 MiB）ごとに送る。
 * すべて送り終えたら upload_id を返すので、呼び出し側は通常のアップロード API に、ファイルの代わりに
 * upload_id をフォームに入れて送る。
 *
 *   POST {endpoint}/chunks                     { filename, size } → { upload_id, chunk_size, chunks }
 *   PUT  {endpoint}/chunks/{upload_id}/{index}  その部分のバイト列
 */

interface StartResponse {
  upload_id: string;
  chunk_size: number;
  chunks: number;
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/** 送り直してよい失敗か（通信エラー・429・5xx）。それ以外（400 など）は送り直しても同じなので諦める。 */
const isRetryable = (err: unknown): boolean => {
  if (!axios.isAxiosError(err)) return false;
  if (!err.response) return true;
  const status = err.response.status;
  return status === 429 || status >= 500;
};

/**
 * file を分割して endpoint（例：/api/games/upload）に送り、upload_id を返す。
 * onProgress には送り終えたバイト数と全体のバイト数を渡す。
 * 1 つの部分の送信は、通信エラー・429・5xx なら間を空けて 4 回まで試す。
 */
export async function uploadInChunks(
  endpoint: string,
  file: File,
  token: string | null,
  onProgress: (loaded: number, total: number) => void,
): Promise<string> {
  const headers: Record<string, string> = token ? { Authorization: `Bearer ${token}` } : {};

  // 1. セッションを作る（ここで保存量・サーバーの空きなどを確かめ、だめなら 4xx / 503 が返る）
  const start = await axios.post<StartResponse>(
    `${endpoint}/chunks`,
    { filename: file.name, size: file.size },
    { headers },
  );
  const { upload_id: uploadId, chunk_size: chunkSize, chunks } = start.data;

  // 2. 部分ごとに順に送る
  for (let index = 0; index < chunks; index++) {
    const begin = index * chunkSize;
    const blob = file.slice(begin, Math.min(file.size, begin + chunkSize));
    for (let attempt = 1; ; attempt++) {
      try {
        await axios.put(`${endpoint}/chunks/${uploadId}/${index}`, blob, {
          headers: { ...headers, 'Content-Type': 'application/octet-stream' },
          onUploadProgress: (e) => onProgress(begin + e.loaded, file.size),
        });
        break;
      } catch (err) {
        if (attempt >= 4 || !isRetryable(err)) {
          throw err;
        }
        // 429 なら Retry-After に従い、それ以外は 2, 4, 8 秒と間を空ける
        const retryAfter = axios.isAxiosError(err) ? Number(err.response?.headers?.['retry-after']) : NaN;
        await sleep(Number.isFinite(retryAfter) && retryAfter > 0 ? Math.min(retryAfter, 60) * 1000 : 1000 * 2 ** attempt);
      }
    }
    onProgress(Math.min(file.size, begin + chunkSize), file.size);
  }
  return uploadId;
}
