import { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import { Container, Typography, Grid, Card, CardContent, CardMedia, Box, CircularProgress, Alert, Avatar, Tabs, Tab, useTheme, TextField } from '@mui/material';
import { Link } from 'react-router-dom';
import VideocamIcon from '@mui/icons-material/Videocam';
import SportsEsportsIcon from '@mui/icons-material/SportsEsports';
import PublicIcon from '@mui/icons-material/Public';

// 一覧に表示するコンテンツ 1 件（動画・ゲーム・静的サイトを共通の形にそろえたもの）。
interface Content {
  id: string;
  title: string;
  thumbnail_url?: string;
  // 投稿者 ID（動画・ゲーム）。型は number だが、API は UUID 文字列を返す
  uploader_id: number;
  // 投稿者 ID（静的サイトはこの名前で返る）
  user_id?: number;
  type: 'video' | 'game' | 'static-site';
  uploader_name?: string;
  uploader_icon?: string;
  created_at: string;
  entry_point_path?: string;
}

/**
 * トップページ。動画・ゲーム・静的サイトの一覧をまとめて取得し、
 * タブ（すべて / 種別ごと）とキーワード検索で表示する。
 */
const HomePage = () => {
  const [allNewContents, setAllNewContents] = useState<Content[]>([]);
  const [videos, setVideos] = useState<Content[]>([]);
  const [games, setGames] = useState<Content[]>([]);
  const [staticSites, setStaticSites] = useState<Content[]>([]);
  // 初回読み込み中かどうか（検索による再取得中は isSearching を使う）
  const [loading, setLoading] = useState(true);
  const [isSearching, setIsSearching] = useState(false);
  const [error, setError] = useState('');
  const [tabIndex, setTabIndex] = useState(0);
  const [searchTerm, setSearchTerm] = useState('');
  // 入力が 500ms 止まった時点で確定する検索語（API 呼び出しに使う）
  const [debouncedSearchTerm, setDebouncedSearchTerm] = useState('');
  const theme = useTheme();
  // staticSiteDomainはHomePageでは直接使わないため削除

  // 検索語の入力をデバウンスし、入力のたびに API を呼ばないようにする。
  useEffect(() => {
    const handler = setTimeout(() => setDebouncedSearchTerm(searchTerm), 500);
    return () => clearTimeout(handler);
  }, [searchTerm]);

  // 確定した検索語が変わるたびに 3 種類の一覧を取得し、投稿者情報を付けて state に保存する。
  useEffect(() => {
    const fetchContents = async () => {
      try {
        setIsSearching(true);
        // 1. 3 種類の一覧を並行取得する（Cache-Control: no-store でキャッシュを使わないよう指定する）
        const query = debouncedSearchTerm ? `?q=${encodeURIComponent(debouncedSearchTerm)}` : '';

        const [videosRes, gamesRes, staticSitesRes] = await Promise.all([
          axios.get(`/api/videos${query}`, { headers: { 'Cache-Control': 'no-store' } }),
          axios.get(`/api/games${query}`, { headers: { 'Cache-Control': 'no-store' } }),
          axios.get(`/api/static-sites${query}`, { headers: { 'Cache-Control': 'no-store' } }),
        ]);

        // 2. 種別を付けて共通の形にそろえる（動画は thumbnail_path をサムネ URL として使う）
        const videoData: Content[] = (videosRes.data || []).map((v: any) => ({ ...v, type: 'video', thumbnail_url: v.thumbnail_path }));
        const gameData: Content[] = (gamesRes.data || []).map((g: any) => ({ ...g, type: 'game' }));
        const staticSiteData: Content[] = (staticSitesRes.data || []).map((s: any) => ({ ...s, type: 'static-site' }));

        // 3. 全種別をまとめて新しい順に並べる
        const combinedContents = [...videoData, ...gameData, ...staticSiteData].sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime());
        
        // 4. 投稿者 ID（uploader_id または user_id）を重複なく集め、プロフィールを取得する
        const uploaderIds = [...new Set(combinedContents.map(c => c.uploader_id || c.user_id).filter(id => id != null && id !== 0))];

        if (uploaderIds.length > 0) {
          // プロフィールが無い投稿者がいても一覧全体を失敗させない（該当者は 'Unknown' 表示）
          const profiles = await Promise.all(uploaderIds.map(id =>
            axios.get(`/api/profile/${id}`)
              .then(res => ({ id, ...res.data }))
              .catch(() => ({ id }))
          ));
          const profileMap = new Map(profiles.map(p => [p.id, p]));

          // 投稿者名・アイコンと、サムネが無い場合の既定画像を付ける
          const enrich = (content: Content) => ({
            ...content,
            uploader_name: profileMap.get(content.uploader_id || content.user_id)?.username || 'Unknown',
            uploader_icon: profileMap.get(content.uploader_id || content.user_id)?.icon_url || '',
            thumbnail_url: content.thumbnail_url || (content.type === 'static-site' ? '/default-static-site.png' : '/placeholder.png'),
          });

          setAllNewContents(combinedContents.map(enrich));
          setVideos(videoData.map(enrich));
          setGames(gameData.map(enrich));
          setStaticSites(staticSiteData.map(enrich));
        } else {
          // 投稿者 ID が 1 件も無い場合は付与処理をしない（既定サムネも付かない）
          setAllNewContents(combinedContents);
          setVideos(videoData);
          setGames(gameData);
          setStaticSites(staticSiteData);
        }
      } catch (err) {
        console.error('Failed to fetch contents:', err);
        setError('コンテンツの読み込みに失敗しました。');
      } finally {
        setLoading(false);
        setIsSearching(false);
      }
    };

    fetchContents();
  }, [debouncedSearchTerm]);

  // タブ切り替えと検索欄入力のハンドラ。
  const handleTabChange = useCallback((_event: React.SyntheticEvent, newValue: number) => setTabIndex(newValue), []);
  const handleSearchChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => setSearchTerm(e.target.value), []);

  // サムネ URL にキャッシュバスタ（v=<URL 自身>）を付与する。
  // v の値は URL から決まるため、同じ URL なら同じ値になり、毎回の再取得にはならない。
  // （バックエンドは毎回ランダム名でサムネを差し替えるため、URL が変われば新しい画像が取得される）。
  const withCacheBuster = useCallback((url: string | undefined) => {
    if (!url) return url;
    const sep = url.includes('?') ? '&' : '?';
    return `${url}${sep}v=${encodeURIComponent(url)}`;
  }, []);

  // 初回読み込みが終わるまではスピナーのみ表示する。
  if (loading) return <CircularProgress />;

  // コンテンツのカードをグリッド表示する。カード全体が詳細ページへのリンクになる。
  const renderContentGrid = (contentArray: Content[]) => (
    <Grid container spacing={3}>
      {contentArray.map((content) => {
        const isStaticSite = content.type === 'static-site';
        // 静的サイトのカードも、サイト本体ではなく詳細ページ（StaticSiteDetailPage）へリンクする
        const linkUrl = isStaticSite 
          ? `/static-sites/${content.id}`
          : `/${content.type}s/${content.id}`;
        const CardComponent = Link; // Linkコンポーネントを直接使用

        return (
          <Grid item key={`${content.type}-${content.id}`} xs={12} sm={6} md={4} lg={3}>
            <Card
              component={CardComponent}
              to={linkUrl}
              sx={{ height: '100%', display: 'flex', flexDirection: 'column', textDecoration: 'none', boxShadow: 1, position: 'relative', '&:hover': { transform: 'translateY(-4px)', boxShadow: 6 }, transition: 'transform 0.2s ease-in-out, box-shadow 0.2s ease-in-out' }}
            >
              <CardMedia image={withCacheBuster(content.thumbnail_url)} sx={{ height: 140, backgroundSize: content.type === 'game' ? 'contain' : 'cover', bgcolor: 'black' }} />
              <CardContent sx={{ flexGrow: 1 }}>
                <Typography gutterBottom variant="h6" component="div" noWrap>{content.title}</Typography>
                <Box sx={{ display: 'flex', alignItems: 'center', mt: 1 }}>
                  <Avatar src={content.uploader_icon || '/default-icon.png'} sx={{ width: 24, height: 24, mr: 1, border: `1px solid ${theme.palette.background.paper}`, boxShadow: `0 0 0 1px ${theme.palette.grey[400]}` }} />
                  <Typography variant="body2" color="text.secondary">{content.uploader_name}</Typography>
                </Box>
              </CardContent>
              {/* 右上に種別アイコンを表示する */}
              <Box sx={{ position: 'absolute', top: 8, right: 8, backgroundColor: 'rgba(100, 100, 100, 0.7)', borderRadius: 1, p: '2px 4px', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                {content.type === 'video' ? <VideocamIcon sx={{ fontSize: 18, color: 'white' }} /> : content.type === 'game' ? <SportsEsportsIcon sx={{ fontSize: 18, color: 'white' }} /> : <PublicIcon sx={{ fontSize: 18, color: 'white' }} />}
              </Box>
            </Card>
          </Grid>
        );
      })}
    </Grid>
  );

  return (
    <Container maxWidth="lg">
      <Box sx={{ my: 4 }}>
        <Typography variant="h4" component="h1" gutterBottom>コンテンツ一覧</Typography>
        <TextField label="検索" variant="outlined" fullWidth value={searchTerm} onChange={handleSearchChange} sx={{ mb: 3 }} />
        {isSearching && <Box sx={{ display: 'flex', justifyContent: 'center', my: 2 }}><CircularProgress size={24} /></Box>}
        <Box sx={{ borderBottom: 1, borderColor: 'divider', mb: 3 }}>
          <Tabs value={tabIndex} onChange={handleTabChange} aria-label="content category tabs">
            <Tab label="すべて" />
            <Tab label="動画" />
            <Tab label="ゲーム" />
            <Tab label="静的サイト" />
          </Tabs>
        </Box>
        {/* エラー時はメッセージのみ、それ以外は選択中のタブの一覧を表示する */}
        {error ? <Alert severity="error">{error}</Alert> : (
          <>
            {tabIndex === 0 && renderContentGrid(allNewContents)}
            {tabIndex === 1 && renderContentGrid(videos)}
            {tabIndex === 2 && renderContentGrid(games)}
            {tabIndex === 3 && renderContentGrid(staticSites)}
          </>
        )}
      </Box>
    </Container>
  );
};

export default HomePage;