import axios from 'axios';

export interface Team {
  id: string;
  token: string;
  name: string;
  description: string | null;
  is_public: boolean;
  auto_approve: boolean;
  allow_member_invite?: boolean;
  created_by: string | null;
  created_at: string;
  _joined?: boolean;
}

export interface TeamPost {
  id: string;
  team_id: string;
  author_id: string;
  author_name: string;
  title: string;
  body: string;
  created_at: string;
}

export interface TeamMember {
  user_id: string;
  role: string;
  joined_at: string;
}

export interface JoinRequest {
  id: string;
  team_id: string;
  user_id: string;
  status: string;
  requested_at: string;
  reviewed_at: string;
  reviewed_by: string;
}

export interface TeamPermission {
  is_public: boolean;
  can_view: boolean;
  can_post: boolean;
  role: string;
}

// axios interceptor (AuthContext) が Authorization ヘッダーを自動付与するため、
// ここでは相対パスのみを指定する。
export const teamApi = {
  listPublic: () => axios.get<Team[]>('/api/teams'),
  listMine: () => axios.get<Team[]>('/api/teams/mine'),
  get: (teamToken: string) => axios.get<Team>(`/api/teams/${teamToken}`),
  create: (data: { name: string; description?: string; is_public?: boolean; auto_approve?: boolean }) =>
    axios.post<Team>('/api/teams', data),
  update: (teamToken: string, data: { name?: string; description?: string; is_public?: boolean; auto_approve?: boolean; allow_member_invite?: boolean }) =>
    axios.patch(`/api/teams/${teamToken}`, data),
  deleteTeam: (teamToken: string) => axios.delete(`/api/teams/${teamToken}`),
  permission: (teamToken: string) => axios.get<TeamPermission>(`/api/teams/${teamToken}/permission`),
  listPosts: (teamToken: string) => axios.get<TeamPost[]>(`/api/teams/${teamToken}/content`),
  getPost: (teamToken: string, postId: string) =>
    axios.get<TeamPost>(`/api/teams/${teamToken}/content/${postId}`),
  createPost: (teamToken: string, data: { title: string; body?: string }) =>
    axios.post<TeamPost>(`/api/teams/${teamToken}/content`, data),
  listMembers: (teamToken: string) => axios.get<TeamMember[]>(`/api/teams/${teamToken}/members`),
  addMember: (teamToken: string, userId: string, role: string) =>
    axios.post(`/api/teams/${teamToken}/members`, { user_id: userId, role }),
  removeMember: (teamToken: string, userId: string) =>
    axios.delete(`/api/teams/${teamToken}/members/${userId}`),
  join: (teamToken: string) => axios.post(`/api/teams/${teamToken}/join`),
  listJoinRequests: (teamToken: string) =>
    axios.get<JoinRequest[]>(`/api/teams/${teamToken}/join-requests`),
  reviewJoinRequest: (teamToken: string, requestId: string, action: 'approve' | 'reject') =>
    axios.patch(`/api/teams/${teamToken}/join-requests/${requestId}`, {}, { params: { action } }),
  cancelJoinRequest: (teamToken: string) =>
    axios.delete(`/api/teams/${teamToken}/join-requests`),
};

export const TEAM_ROLE_RANK: Record<string, number> = { owner: 3, admin: 2, member: 1 };

// ------------------------------------------------------------------
// コンテンツ (動画 / ゲーム / 静的サイト) — チーム共有コンテンツ管理用
// ------------------------------------------------------------------
// axios interceptor (AuthContext) が Authorization ヘッダーを自動付与するため、
// ここでは相対パスのみを指定する。
// - 一覧は `?team=<チームトークン>` でそのチーム限定、`?q=<検索語>` でタイトル検索。
// - 投稿時は multipart フォームの `team_id` フィールドにチームトークンを含める。

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

// コンテンツ種別ごとのアップロードフォームのフィールド名
export const CONTENT_UPLOAD_FIELDS = {
  videos: { fileField: 'video', endpoint: '/api/videos/upload' },
  games: { fileField: 'game', endpoint: '/api/games/upload' },
  sites: { fileField: 'file', endpoint: '/api/static-sites/upload' },
};