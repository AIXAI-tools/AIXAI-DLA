# AIXAI temporary yt-dlp extractor fix (loaded with --plugin-dirs).
#
# The page no longer embeds the initial state the bundled extractor reads.
# _patched_real_extract and _extract_part are copied unchanged from
# yt-dlp pull request #17898 (commit 9fd15ea9ff81, released under the
# Unlicense). The bundled extractor always runs first; the copied code is
# used only when it fails with "Unable to extract initial state".
# Once the bundled yt-dlp has _extract_part (fix merged), nothing is
# overridden.

from yt_dlp.extractor.bilibili import *  # noqa: F403 - names used by the copied methods
from yt_dlp.extractor.bilibili import BiliBiliIE as _BundledBiliBiliIE
from yt_dlp.utils import ExtractorError

__all__ = []

if not hasattr(_BundledBiliBiliIE, '_extract_part'):
    class AIXAIBiliBiliIE(_BundledBiliBiliIE, plugin_name='aixai'):
        _VALID_URL = r'https?://(?:www\.)?bilibili\.com/(?:[a-z]{2}(?:-Hans)?/)?(?:video/|festival/[^/?#]+\?(?:[^#]*&)?bvid=)(?P<prefix>[aAbB][vV])(?P<id>[^/?#&]+)'

        def _real_extract(self, url):
            try:
                return super()._real_extract(url)
            except ExtractorError as e:
                if 'Unable to extract initial state' not in str(e.orig_msg or e.msg):
                    raise
            self.to_screen('Bundled extractor could not read the page; using the updated extractor')
            return self._patched_real_extract(url)

        def _patched_real_extract(self, url):
            video_id, prefix = self._match_valid_url(url).group('id', 'prefix')
            headers = self.geo_verification_headers()
            webpage, urlh = self._download_webpage_handle(url, video_id, headers=headers)
            if not self._match_valid_url(urlh.url):
                return self.url_result(urlh.url)

            headers.update({
                'Referer': 'https://www.bilibili.com/',
                'Origin': 'https://www.bilibili.com',
            })

            is_festival = False
            metainfo = {'http_headers': self._HEADERS}
            initial_state = self._search_json(r'window\.__INITIAL_STATE__\s*=', webpage, 'initial state', video_id, default=None)
            if not initial_state:
                if self._search_json(r'\bwindow\._riskdata_\s*=', webpage, 'risk', video_id, default={}).get('v_voucher'):
                    raise ExtractorError('You have exceeded the rate limit. Try again later', expected=True)
                query = {'platform': 'web'}
                prefix = prefix.upper()
                if prefix == 'BV':
                    query['bvid'] = prefix + video_id
                elif prefix == 'AV':
                    query['aid'] = video_id
                detail = traverse_obj(self._download_json(
                    'https://api.bilibili.com/x/web-interface/wbi/view/detail', video_id,
                    note='Downloading video details', errnote='Failed to download video details',
                    query=self._sign_wbi(query, video_id), headers=headers), 'data', expected_type=dict)
                if detail is None:
                    raise ExtractorError('Unable to extract video data')

                video_data = detail['View']
                if tags := traverse_obj(detail, ('Tags', ..., 'tag_name')):
                    metainfo['tags'] = tags
            else:
                if traverse_obj(initial_state, ('error', 'trueCode')) == -403:
                    self.raise_login_required()
                if traverse_obj(initial_state, ('error', 'trueCode')) == -404:
                    raise ExtractorError(
                        'This video may be deleted or geo-restricted. '
                        'You might want to try a VPN or a proxy server (with --proxy)', expected=True)

                if 'videoData' in initial_state:
                    video_data = initial_state['videoData']
                else:
                    video_data = initial_state['videoInfo']
                    is_festival = True

            new_url = traverse_obj(video_data, ('redirect_url', {url_or_none}))
            if new_url and BiliBiliBangumiIE.suitable(new_url):
                return self.url_result(new_url, BiliBiliBangumiIE)

            video_id, title = video_data['bvid'], video_data.get('title')

            festival_info = {}
            if is_festival:
                festival_info = traverse_obj(initial_state, {
                    'uploader': ('videoInfo', 'upName'),
                    'uploader_id': ('videoInfo', 'upMid', {str_or_none}),
                    'like_count': ('videoStatus', 'like', {int_or_none}),
                    'thumbnail': ('sectionEpisodes', lambda _, v: v['bvid'] == video_id, 'cover'),
                }, get_all=False)

                is_anthology = False
            else:
                page_list = video_data.get('pages')
                if page_list is None:
                    page_list = traverse_obj(
                        self._download_json(
                            'https://api.bilibili.com/x/player/pagelist', video_id,
                            query={'bvid': video_id, 'jsonp': 'jsonp'},
                            note='Extracting videos in anthology', headers=headers),
                        'data', expected_type=list)
                    is_anthology = page_list is not None and len(page_list) > 1
                else:
                    is_anthology = len(page_list) > 1

            metainfo = {
                'title': title,
                **metainfo,
                **traverse_obj(initial_state, {
                    'uploader': ('upData', 'name'),
                    'uploader_id': ('upData', 'mid', {str_or_none}),
                    'tags': ('tags', ..., 'tag_name'),
                }),
                **traverse_obj(video_data, {
                    'uploader': ('owner', 'name'),
                    'uploader_id': ('owner', 'mid', {str_or_none}),
                    'like_count': ('stat', 'like', {int_or_none}),
                    'thumbnail': ('pic', {url_or_none}),
                }),
                **festival_info,
                **traverse_obj(video_data, {
                    'description': 'desc',
                    'timestamp': ('pubdate', {int_or_none}),
                    'view_count': (('viewCount', ('stat', 'view')), {int_or_none}),
                    'comment_count': ('stat', 'reply', {int_or_none}),
                }, get_all=False),
            }

            # Bilibili anthologies are similar to playlists but all videos share the same video ID as the anthology itself.
            part_id = int_or_none(parse_qs(url).get('p', [None])[-1])
            part_info = None
            if is_anthology:
                if part_id is None:
                    if self._yes_playlist(video_id, video_id):
                        def gen_entry(idx):
                            info = page_list[idx]
                            p = idx + 1
                            return (self._extract_part(
                                initial_state, video_data, video_id, metainfo,
                                webpage=webpage if p == part_id else None, headers=headers,
                                part_id=p, part_info=info),)

                        return self.playlist_result(
                            InAdvancePagedList(gen_entry, len(page_list), 1),
                            video_id, title, **metainfo)
                    part_id = 1

                part_info = page_list[part_id - 1]

            return self._extract_part(
                initial_state, video_data, video_id, metainfo,
                webpage=webpage, headers=headers, part_id=part_id, part_info=part_info)

        def _extract_part(self, initial_state, video_data, video_id, metainfo, webpage=None, headers=None, part_id=None, part_info=None):
            title = metainfo['title']
            if part_info is not None:
                title += f' p{part_id:02d} {traverse_obj(part_info, "part") or ""}'
                cid = part_info['cid']
            else:
                cid = video_data['cid']

            aid = video_data.get('aid')
            old_video_id = format_field(aid, None, f'%s_part{part_id or 1}')

            metainfo = {
                **metainfo,
                'title': title,
                'id': f'{video_id}{format_field(part_id, None, "_p%d")}',
                '_old_archive_ids': [make_archive_id(self, old_video_id)] if old_video_id else None,
            }

            is_interactive = traverse_obj(video_data, ('rights', 'is_stein_gate'))
            if is_interactive:
                return self.playlist_result(
                    self._get_interactive_entries(video_id, cid, metainfo, headers=headers), **metainfo,
                    duration=traverse_obj(video_data, ('duration', {int_or_none})),
                    __post_extractor=self.extract_comments(aid))

            play_info = traverse_obj(
                self._search_json(r'window\.__playinfo__\s*=', webpage, 'play info', video_id, default=None),
                ('data', {dict}))
            if not self.is_logged_in or not play_info:
                if dl_play_info := self._download_playinfo(video_id, cid, headers=headers, query={'try_look': 1}, fatal=False):
                    play_info = dl_play_info
            formats = self.extract_formats(play_info)

            if video_data.get('is_upower_exclusive'):
                high_level = traverse_obj(initial_state, ('elecFullInfo', 'show_info', 'high_level', {dict})) or {}
                msg = f'{join_nonempty("title", "sub_title", from_dict=high_level, delim="，")}. {self._login_hint()}'
                if not formats:
                    raise ExtractorError(f'This is a supporter-only video: {msg}', expected=True)
                if '试看' in traverse_obj(play_info, ('accept_description', ..., {str})):
                    self.report_warning(
                        f'This is a supporter-only video, only the preview will be extracted: {msg}',
                        video_id=video_id)

            if not traverse_obj(play_info, 'dash'):
                # we only have legacy formats and need additional work
                has_qn = lambda x: x in traverse_obj(formats, (..., 'quality'))
                for qn in traverse_obj(play_info, ('accept_quality', lambda _, v: not has_qn(v), {int})):
                    formats.extend(traverse_obj(
                        self.extract_formats(self._download_playinfo(video_id, cid, headers=headers, query={'qn': qn})),
                        lambda _, v: not has_qn(v['quality'])))
                self._check_missing_formats(play_info, formats)
                flv_formats = traverse_obj(formats, lambda _, v: v['fragments'])
                if flv_formats and len(flv_formats) < len(formats):
                    # Flv and mp4 are incompatible due to `multi_video` workaround, so drop one
                    if not self._configuration_arg('prefer_multi_flv'):
                        dropped_fmts = ', '.join(
                            f'{f.get("format_note")} ({f.get("format_id")})' for f in flv_formats)
                        formats = traverse_obj(formats, lambda _, v: not v.get('fragments'))
                        if dropped_fmts:
                            self.to_screen(
                                f'Dropping incompatible flv format(s) {dropped_fmts} since mp4 is available. '
                                'To extract flv, pass --extractor-args "bilibili:prefer_multi_flv"')
                    else:
                        formats = traverse_obj(
                            # XXX: Filtering by extractor-arg is for testing purposes
                            formats, lambda _, v: v['quality'] == int(self._configuration_arg('prefer_multi_flv')[0]),
                        ) or [max(flv_formats, key=lambda x: x['quality'])]

            if traverse_obj(formats, (0, 'fragments')):
                # We have flv formats, which are individual short videos with their own timestamps and metainfo
                # Binary concatenation corrupts their timestamps, so we need a `multi_video` workaround
                return {
                    **metainfo,
                    '_type': 'multi_video',
                    'entries': [{
                        'id': f'{metainfo["id"]}_{idx}',
                        'title': metainfo['title'],
                        'http_headers': metainfo['http_headers'],
                        'formats': [{
                            **fragment,
                            'format_id': formats[0].get('format_id'),
                        }],
                        'subtitles': self.extract_subtitles(video_id, cid) if idx == 0 else None,
                        '__post_extractor': self.extract_comments(aid) if idx == 0 else None,
                    } for idx, fragment in enumerate(formats[0]['fragments'])],
                    'duration': traverse_obj(play_info, ('timelength', {float_or_none(scale=1000)})),
                }

            return {
                **metainfo,
                'formats': formats,
                'duration': traverse_obj(play_info, ('timelength', {float_or_none(scale=1000)})),
                'chapters': self._get_chapters(aid, cid),
                'subtitles': self.extract_subtitles(video_id, cid),
                '__post_extractor': self.extract_comments(aid),
            }

    __all__ = ['AIXAIBiliBiliIE']
