import axios from 'axios';

/** team-service が返すチーム情報。 */
export interface Team {
  id: string;
  // URL・API のパスでチームを指定するときに使う識別子
  token: string;
  name: string;
  description: string | null;
  is_public: boolean;
  auto_approve: boolean;
  allow_member_invite?: boolean;
  created_by: string | null;
  created_at: string;
  // フロントエンド側で付ける「参加済み」フラグ（API の応答には含まれない）
  _joined?: boolean;
}

/** チームメンバー 1 人分の情報。 */
export interface TeamMember {
  user_id: string;
  role: string;
  joined_at: string;
}

/** チームへの参加申請。 */
export interface JoinRequest {
  id: string;
  team_id: string;
  user_id: string;
  status: string;
  requested_at: string;
  reviewed_at: string;
  reviewed_by: string;
}

// axios interceptor (AuthContext) が Authorization ヘッダーを自動付与するため、
// ここでは相対パスのみを指定する。
export const teamApi = {
  // 公開チームの一覧（認証不要）
  listPublic: () => axios.get<Team[]>('/api/teams'),
  // 自分が所属するチームの一覧
  listMine: () => axios.get<Team[]>('/api/teams/mine'),
  get: (teamToken: string) => axios.get<Team>(`/api/teams/${teamToken}`),
  // チームを作成する
  create: (data: { name: string; description?: string; is_public?: boolean; auto_approve?: boolean }) =>
    axios.post<Team>('/api/teams', data),
  // チーム設定を部分更新する（PATCH）
  update: (teamToken: string, data: { name?: string; description?: string; is_public?: boolean; auto_approve?: boolean; allow_member_invite?: boolean }) =>
    axios.patch(`/api/teams/${teamToken}`, data),
  deleteTeam: (teamToken: string) => axios.delete(`/api/teams/${teamToken}`),
  // メンバー一覧を取得する
  listMembers: (teamToken: string) => axios.get<TeamMember[]>(`/api/teams/${teamToken}/members`),
  // ユーザー ID とロールを指定してメンバーを追加する
  addMember: (teamToken: string, userId: string, role: string) =>
    axios.post(`/api/teams/${teamToken}/members`, { user_id: userId, role }),
  // 自分の userId を渡すとチームから退出する（最後の owner は 409）
  removeMember: (teamToken: string, userId: string) =>
    axios.delete(`/api/teams/${teamToken}/members/${userId}`),
  // status: 'approved' | 'already_member' | 'pending'。保留中の申請が既にあれば 409。
  join: (teamToken: string) =>
    axios.post<{ status: 'approved' | 'already_member' | 'pending'; role?: string; request_id?: string }>(
      `/api/teams/${teamToken}/join`
    ),
  // 参加申請の一覧を取得する
  listJoinRequests: (teamToken: string) =>
    axios.get<JoinRequest[]>(`/api/teams/${teamToken}/join-requests`),
  // 参加申請を承認または却下する（action はクエリパラメータで渡す）
  reviewJoinRequest: (teamToken: string, requestId: string, action: 'approve' | 'reject') =>
    axios.patch(`/api/teams/${teamToken}/join-requests/${requestId}`, {}, { params: { action } }),
};

// ロールの強さ。数値が大きいほど権限が強い（未知のロールは呼び出し側で 0 として扱う）。
export const TEAM_ROLE_RANK: Record<string, number> = { owner: 3, admin: 2, member: 1 };

// ロールの一覧（強い順）。
export const TEAM_ROLES = ['owner', 'admin', 'member'] as const;

// team-service の canGrantRole と同じ規則: 自分より弱いロールだけ付与できる。
// 例外として owner は owner を付与できる（オーナー権限の移譲・共同オーナー化）。
export const canGrantTeamRole = (myRole: string, role: string): boolean => {
  if (myRole === 'owner' && role === 'owner') return true;
  return (TEAM_ROLE_RANK[role] ?? 0) < (TEAM_ROLE_RANK[myRole] ?? 0);
};

// 自分より弱いロールのメンバーだけ、ロール変更・削除できる。
// また、自分が admin 以上であることも条件とする。
export const canManageTeamMember = (myRole: string, targetRole: string): boolean =>
  (TEAM_ROLE_RANK[myRole] ?? 0) >= TEAM_ROLE_RANK.admin &&
  (TEAM_ROLE_RANK[targetRole] ?? 0) < (TEAM_ROLE_RANK[myRole] ?? 0);

// ------------------------------------------------------------------
// コンテンツ (動画 / ゲーム / 静的サイト) — チーム共有コンテンツ管理用
// ------------------------------------------------------------------
// axios interceptor (AuthContext) が Authorization ヘッダーを自動付与するため、
// ここでは相対パスのみを指定する。
// - 一覧は `?team=<チームトークン>` でそのチーム限定、`?q=<検索語>` でタイトル検索。
// - 投稿時は multipart フォームの `team_id` フィールドにチームトークンを含める。

/** チーム共有コンテンツ一覧の 1 件（動画・ゲーム・静的サイト共通の形）。 */
export interface TeamContentItem {
  id: string;
  title: string;
  description: string | null;
  // 動画は thumbnail_path、ゲーム/サイトは thumbnail_url で返る
  thumbnail_path?: string | null;
  thumbnail_url?: string | null;
  status: string;
  created_at: string;
  uploader_id?: string | null;
  // 動画専用
  filename?: string | null;
  // ゲーム専用
  game_url?: string | null;
  scale?: number;
  // 静的サイト専用
  entry_point_path?: string | null;
}

// コンテンツ種別
export type TeamContentType = 'videos' | 'games' | 'sites';

// チーム共有コンテンツの一覧取得・削除 API。
export const contentApi = {
  // 一覧 (チーム限定 + 検索)
  listVideos: (teamToken: string, search?: string) =>
    axios.get<TeamContentItem[]>(`/api/videos`, { params: { team: teamToken, q: search } }),
  listGames: (teamToken: string, search?: string) =>
    axios.get<TeamContentItem[]>(`/api/games`, { params: { team: teamToken, q: search } }),
  listStaticSites: (teamToken: string, search?: string) =>
    axios.get<TeamContentItem[]>(`/api/static-sites`, { params: { team: teamToken, q: search } }),

  // 削除 (owner-or-admin: uploader_id == userID || isAdmin)
  deleteVideo: (id: string) => axios.delete(`/api/videos/delete/${id}`),
  deleteGame: (id: string) => axios.delete(`/api/games/${id}`),
  deleteStaticSite: (id: string) => axios.delete(`/api/static-sites/${id}`),
};

// コンテンツ種別ごとの詳細/編集ページへのリンク
export const CONTENT_LINKS = {
  videos: {
    detail: (id: string) => `/videos/${id}`,
    edit: (id: string) => `/edit-video/${id}`,
  },
  games: {
    detail: (id: string) => `/games/${id}`,
    edit: (id: string) => `/edit-game/${id}`,
  },
  sites: {
    detail: (id: string) => `/static-sites/${id}`,
    edit: (id: string) => `/edit-static-site/${id}`,
  },
};