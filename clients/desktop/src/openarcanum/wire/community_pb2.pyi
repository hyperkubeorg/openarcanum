from openarcanum.wire import call_pb2 as _call_pb2
from openarcanum.wire import session_pb2 as _session_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class RpcErrorCode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    RPC_ERROR_CODE_UNSPECIFIED: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_PERMISSION_DENIED: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_NOT_FOUND: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_INVALID: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_NOT_MEMBER: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_RATE_LIMITED: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_TOO_LARGE: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_CONFLICT: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_INTERNAL: _ClassVar[RpcErrorCode]
    RPC_ERROR_CODE_DISABLED: _ClassVar[RpcErrorCode]

class MemberStatus(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    MEMBER_STATUS_UNSPECIFIED: _ClassVar[MemberStatus]
    MEMBER_STATUS_UNKNOWN: _ClassVar[MemberStatus]
    MEMBER_STATUS_ACTIVE: _ClassVar[MemberStatus]
    MEMBER_STATUS_PENDING: _ClassVar[MemberStatus]
    MEMBER_STATUS_REMOVED: _ClassVar[MemberStatus]
    MEMBER_STATUS_BANNED: _ClassVar[MemberStatus]
    MEMBER_STATUS_DEVICE_REVOKED: _ClassVar[MemberStatus]
    MEMBER_STATUS_DEVICE_LIMIT: _ClassVar[MemberStatus]

class Visibility(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    VISIBILITY_UNSPECIFIED: _ClassVar[Visibility]
    VISIBILITY_PRIVATE: _ClassVar[Visibility]
    VISIBILITY_PUBLIC: _ClassVar[Visibility]

class JoinPolicy(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    JOIN_POLICY_UNSPECIFIED: _ClassVar[JoinPolicy]
    JOIN_POLICY_INVITE_ONLY: _ClassVar[JoinPolicy]
    JOIN_POLICY_REQUEST: _ClassVar[JoinPolicy]
    JOIN_POLICY_OPEN: _ClassVar[JoinPolicy]

class AuditVisibility(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    AUDIT_VISIBILITY_UNSPECIFIED: _ClassVar[AuditVisibility]
    AUDIT_VISIBILITY_ADMINS: _ClassVar[AuditVisibility]
    AUDIT_VISIBILITY_EVERYONE: _ClassVar[AuditVisibility]

class RelayMode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    RELAY_MODE_UNSPECIFIED: _ClassVar[RelayMode]
    RELAY_MODE_AUTOMATIC: _ClassVar[RelayMode]
    RELAY_MODE_PINNED: _ClassVar[RelayMode]

class AttachmentKinds(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ATTACHMENT_KINDS_UNSPECIFIED: _ClassVar[AttachmentKinds]
    ATTACHMENT_KINDS_ALL: _ClassVar[AttachmentKinds]
    ATTACHMENT_KINDS_IMAGES_ONLY: _ClassVar[AttachmentKinds]

class ChannelToggle(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    CHANNEL_TOGGLE_INHERIT: _ClassVar[ChannelToggle]
    CHANNEL_TOGGLE_OFF: _ClassVar[ChannelToggle]

class ChannelKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    CHANNEL_KIND_UNSPECIFIED: _ClassVar[ChannelKind]
    CHANNEL_KIND_TEXT: _ClassVar[ChannelKind]
    CHANNEL_KIND_VOICE: _ClassVar[ChannelKind]

class SystemEventKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SYSTEM_EVENT_KIND_UNSPECIFIED: _ClassVar[SystemEventKind]
    SYSTEM_EVENT_KIND_JOINED: _ClassVar[SystemEventKind]
    SYSTEM_EVENT_KIND_LEFT: _ClassVar[SystemEventKind]

class JoinStatus(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    JOIN_STATUS_UNSPECIFIED: _ClassVar[JoinStatus]
    JOIN_STATUS_ADMITTED: _ClassVar[JoinStatus]
    JOIN_STATUS_PENDING: _ClassVar[JoinStatus]
    JOIN_STATUS_REFUSED: _ClassVar[JoinStatus]

class RefusalReason(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    REFUSAL_REASON_UNSPECIFIED: _ClassVar[RefusalReason]
    REFUSAL_REASON_KEY_INVALID: _ClassVar[RefusalReason]
    REFUSAL_REASON_KEY_REVOKED: _ClassVar[RefusalReason]
    REFUSAL_REASON_KEY_EXPIRED: _ClassVar[RefusalReason]
    REFUSAL_REASON_KEY_EXHAUSTED: _ClassVar[RefusalReason]
    REFUSAL_REASON_BANNED: _ClassVar[RefusalReason]
    REFUSAL_REASON_FULL: _ClassVar[RefusalReason]
    REFUSAL_REASON_INVITE_REQUIRED: _ClassVar[RefusalReason]
    REFUSAL_REASON_DENIED: _ClassVar[RefusalReason]

class NetworkBlockStatus(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    NETWORK_BLOCK_STATUS_UNSPECIFIED: _ClassVar[NetworkBlockStatus]
    NETWORK_BLOCK_STATUS_BLOCKED: _ClassVar[NetworkBlockStatus]
    NETWORK_BLOCK_STATUS_PENDING: _ClassVar[NetworkBlockStatus]
    NETWORK_BLOCK_STATUS_SKIPPED: _ClassVar[NetworkBlockStatus]

class ReportKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    REPORT_KIND_UNSPECIFIED: _ClassVar[ReportKind]
    REPORT_KIND_MEMBER: _ClassVar[ReportKind]
    REPORT_KIND_AUTOMOD: _ClassVar[ReportKind]

class ReportStatus(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    REPORT_STATUS_UNSPECIFIED: _ClassVar[ReportStatus]
    REPORT_STATUS_OPEN: _ClassVar[ReportStatus]
    REPORT_STATUS_RESOLVED: _ClassVar[ReportStatus]
    REPORT_STATUS_DISMISSED: _ClassVar[ReportStatus]

class AutoModTrigger(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    AUTOMOD_TRIGGER_UNSPECIFIED: _ClassVar[AutoModTrigger]
    AUTOMOD_TRIGGER_KEYWORD: _ClassVar[AutoModTrigger]
    AUTOMOD_TRIGGER_MENTION_SPAM: _ClassVar[AutoModTrigger]
    AUTOMOD_TRIGGER_LINKS: _ClassVar[AutoModTrigger]
RPC_ERROR_CODE_UNSPECIFIED: RpcErrorCode
RPC_ERROR_CODE_PERMISSION_DENIED: RpcErrorCode
RPC_ERROR_CODE_NOT_FOUND: RpcErrorCode
RPC_ERROR_CODE_INVALID: RpcErrorCode
RPC_ERROR_CODE_NOT_MEMBER: RpcErrorCode
RPC_ERROR_CODE_RATE_LIMITED: RpcErrorCode
RPC_ERROR_CODE_TOO_LARGE: RpcErrorCode
RPC_ERROR_CODE_CONFLICT: RpcErrorCode
RPC_ERROR_CODE_INTERNAL: RpcErrorCode
RPC_ERROR_CODE_DISABLED: RpcErrorCode
MEMBER_STATUS_UNSPECIFIED: MemberStatus
MEMBER_STATUS_UNKNOWN: MemberStatus
MEMBER_STATUS_ACTIVE: MemberStatus
MEMBER_STATUS_PENDING: MemberStatus
MEMBER_STATUS_REMOVED: MemberStatus
MEMBER_STATUS_BANNED: MemberStatus
MEMBER_STATUS_DEVICE_REVOKED: MemberStatus
MEMBER_STATUS_DEVICE_LIMIT: MemberStatus
VISIBILITY_UNSPECIFIED: Visibility
VISIBILITY_PRIVATE: Visibility
VISIBILITY_PUBLIC: Visibility
JOIN_POLICY_UNSPECIFIED: JoinPolicy
JOIN_POLICY_INVITE_ONLY: JoinPolicy
JOIN_POLICY_REQUEST: JoinPolicy
JOIN_POLICY_OPEN: JoinPolicy
AUDIT_VISIBILITY_UNSPECIFIED: AuditVisibility
AUDIT_VISIBILITY_ADMINS: AuditVisibility
AUDIT_VISIBILITY_EVERYONE: AuditVisibility
RELAY_MODE_UNSPECIFIED: RelayMode
RELAY_MODE_AUTOMATIC: RelayMode
RELAY_MODE_PINNED: RelayMode
ATTACHMENT_KINDS_UNSPECIFIED: AttachmentKinds
ATTACHMENT_KINDS_ALL: AttachmentKinds
ATTACHMENT_KINDS_IMAGES_ONLY: AttachmentKinds
CHANNEL_TOGGLE_INHERIT: ChannelToggle
CHANNEL_TOGGLE_OFF: ChannelToggle
CHANNEL_KIND_UNSPECIFIED: ChannelKind
CHANNEL_KIND_TEXT: ChannelKind
CHANNEL_KIND_VOICE: ChannelKind
SYSTEM_EVENT_KIND_UNSPECIFIED: SystemEventKind
SYSTEM_EVENT_KIND_JOINED: SystemEventKind
SYSTEM_EVENT_KIND_LEFT: SystemEventKind
JOIN_STATUS_UNSPECIFIED: JoinStatus
JOIN_STATUS_ADMITTED: JoinStatus
JOIN_STATUS_PENDING: JoinStatus
JOIN_STATUS_REFUSED: JoinStatus
REFUSAL_REASON_UNSPECIFIED: RefusalReason
REFUSAL_REASON_KEY_INVALID: RefusalReason
REFUSAL_REASON_KEY_REVOKED: RefusalReason
REFUSAL_REASON_KEY_EXPIRED: RefusalReason
REFUSAL_REASON_KEY_EXHAUSTED: RefusalReason
REFUSAL_REASON_BANNED: RefusalReason
REFUSAL_REASON_FULL: RefusalReason
REFUSAL_REASON_INVITE_REQUIRED: RefusalReason
REFUSAL_REASON_DENIED: RefusalReason
NETWORK_BLOCK_STATUS_UNSPECIFIED: NetworkBlockStatus
NETWORK_BLOCK_STATUS_BLOCKED: NetworkBlockStatus
NETWORK_BLOCK_STATUS_PENDING: NetworkBlockStatus
NETWORK_BLOCK_STATUS_SKIPPED: NetworkBlockStatus
REPORT_KIND_UNSPECIFIED: ReportKind
REPORT_KIND_MEMBER: ReportKind
REPORT_KIND_AUTOMOD: ReportKind
REPORT_STATUS_UNSPECIFIED: ReportStatus
REPORT_STATUS_OPEN: ReportStatus
REPORT_STATUS_RESOLVED: ReportStatus
REPORT_STATUS_DISMISSED: ReportStatus
AUTOMOD_TRIGGER_UNSPECIFIED: AutoModTrigger
AUTOMOD_TRIGGER_KEYWORD: AutoModTrigger
AUTOMOD_TRIGGER_MENTION_SPAM: AutoModTrigger
AUTOMOD_TRIGGER_LINKS: AutoModTrigger

class ClientMessage(_message.Message):
    __slots__ = ("id", "get_info", "claim", "join", "sync", "history", "post", "edit", "delete", "leave", "set_profile", "admin", "list_members", "list_join_keys", "list_pending", "list_bans", "audit_log", "search", "upload_begin", "upload_chunk", "upload_finish", "file_get", "list_devices", "revoke_device", "publish_contact_card", "get_contact_card", "voice_join", "voice_state", "voice_leave", "check_status", "add_key_packages", "get_avatar", "adopt_server", "react", "pin", "list_pins", "typing", "update_profile", "report_message", "list_reports", "list_automod_rules", "get_emoji", "emoji_upload", "set_status_device", "set_status", "list_statuses", "send_status_keys", "fetch_status_keys", "request_status_key")
    ID_FIELD_NUMBER: _ClassVar[int]
    GET_INFO_FIELD_NUMBER: _ClassVar[int]
    CLAIM_FIELD_NUMBER: _ClassVar[int]
    JOIN_FIELD_NUMBER: _ClassVar[int]
    SYNC_FIELD_NUMBER: _ClassVar[int]
    HISTORY_FIELD_NUMBER: _ClassVar[int]
    POST_FIELD_NUMBER: _ClassVar[int]
    EDIT_FIELD_NUMBER: _ClassVar[int]
    DELETE_FIELD_NUMBER: _ClassVar[int]
    LEAVE_FIELD_NUMBER: _ClassVar[int]
    SET_PROFILE_FIELD_NUMBER: _ClassVar[int]
    ADMIN_FIELD_NUMBER: _ClassVar[int]
    LIST_MEMBERS_FIELD_NUMBER: _ClassVar[int]
    LIST_JOIN_KEYS_FIELD_NUMBER: _ClassVar[int]
    LIST_PENDING_FIELD_NUMBER: _ClassVar[int]
    LIST_BANS_FIELD_NUMBER: _ClassVar[int]
    AUDIT_LOG_FIELD_NUMBER: _ClassVar[int]
    SEARCH_FIELD_NUMBER: _ClassVar[int]
    UPLOAD_BEGIN_FIELD_NUMBER: _ClassVar[int]
    UPLOAD_CHUNK_FIELD_NUMBER: _ClassVar[int]
    UPLOAD_FINISH_FIELD_NUMBER: _ClassVar[int]
    FILE_GET_FIELD_NUMBER: _ClassVar[int]
    LIST_DEVICES_FIELD_NUMBER: _ClassVar[int]
    REVOKE_DEVICE_FIELD_NUMBER: _ClassVar[int]
    PUBLISH_CONTACT_CARD_FIELD_NUMBER: _ClassVar[int]
    GET_CONTACT_CARD_FIELD_NUMBER: _ClassVar[int]
    VOICE_JOIN_FIELD_NUMBER: _ClassVar[int]
    VOICE_STATE_FIELD_NUMBER: _ClassVar[int]
    VOICE_LEAVE_FIELD_NUMBER: _ClassVar[int]
    CHECK_STATUS_FIELD_NUMBER: _ClassVar[int]
    ADD_KEY_PACKAGES_FIELD_NUMBER: _ClassVar[int]
    GET_AVATAR_FIELD_NUMBER: _ClassVar[int]
    ADOPT_SERVER_FIELD_NUMBER: _ClassVar[int]
    REACT_FIELD_NUMBER: _ClassVar[int]
    PIN_FIELD_NUMBER: _ClassVar[int]
    LIST_PINS_FIELD_NUMBER: _ClassVar[int]
    TYPING_FIELD_NUMBER: _ClassVar[int]
    UPDATE_PROFILE_FIELD_NUMBER: _ClassVar[int]
    REPORT_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    LIST_REPORTS_FIELD_NUMBER: _ClassVar[int]
    LIST_AUTOMOD_RULES_FIELD_NUMBER: _ClassVar[int]
    GET_EMOJI_FIELD_NUMBER: _ClassVar[int]
    EMOJI_UPLOAD_FIELD_NUMBER: _ClassVar[int]
    SET_STATUS_DEVICE_FIELD_NUMBER: _ClassVar[int]
    SET_STATUS_FIELD_NUMBER: _ClassVar[int]
    LIST_STATUSES_FIELD_NUMBER: _ClassVar[int]
    SEND_STATUS_KEYS_FIELD_NUMBER: _ClassVar[int]
    FETCH_STATUS_KEYS_FIELD_NUMBER: _ClassVar[int]
    REQUEST_STATUS_KEY_FIELD_NUMBER: _ClassVar[int]
    id: int
    get_info: GetInfo
    claim: Claim
    join: Join
    sync: Sync
    history: History
    post: Post
    edit: Edit
    delete: DeleteMessage
    leave: Leave
    set_profile: SetProfile
    admin: SignedAction
    list_members: ListMembers
    list_join_keys: ListJoinKeys
    list_pending: ListPending
    list_bans: ListBans
    audit_log: AuditQuery
    search: SearchQuery
    upload_begin: UploadBegin
    upload_chunk: UploadChunk
    upload_finish: UploadFinish
    file_get: FileGet
    list_devices: ListDevices
    revoke_device: SignedDeviceRevocation
    publish_contact_card: PublishContactCard
    get_contact_card: GetContactCard
    voice_join: VoiceJoin
    voice_state: VoiceState
    voice_leave: VoiceLeave
    check_status: CheckStatus
    add_key_packages: AddKeyPackages
    get_avatar: GetAvatar
    adopt_server: AdoptServer
    react: React
    pin: Pin
    list_pins: ListPins
    typing: Typing
    update_profile: UpdateProfile
    report_message: ReportMessage
    list_reports: ListReports
    list_automod_rules: ListAutoModRules
    get_emoji: GetEmoji
    emoji_upload: EmojiUpload
    set_status_device: SetStatusDevice
    set_status: SetStatus
    list_statuses: ListStatuses
    send_status_keys: SendStatusKeys
    fetch_status_keys: FetchStatusKeys
    request_status_key: RequestStatusKey
    def __init__(self, id: _Optional[int] = ..., get_info: _Optional[_Union[GetInfo, _Mapping]] = ..., claim: _Optional[_Union[Claim, _Mapping]] = ..., join: _Optional[_Union[Join, _Mapping]] = ..., sync: _Optional[_Union[Sync, _Mapping]] = ..., history: _Optional[_Union[History, _Mapping]] = ..., post: _Optional[_Union[Post, _Mapping]] = ..., edit: _Optional[_Union[Edit, _Mapping]] = ..., delete: _Optional[_Union[DeleteMessage, _Mapping]] = ..., leave: _Optional[_Union[Leave, _Mapping]] = ..., set_profile: _Optional[_Union[SetProfile, _Mapping]] = ..., admin: _Optional[_Union[SignedAction, _Mapping]] = ..., list_members: _Optional[_Union[ListMembers, _Mapping]] = ..., list_join_keys: _Optional[_Union[ListJoinKeys, _Mapping]] = ..., list_pending: _Optional[_Union[ListPending, _Mapping]] = ..., list_bans: _Optional[_Union[ListBans, _Mapping]] = ..., audit_log: _Optional[_Union[AuditQuery, _Mapping]] = ..., search: _Optional[_Union[SearchQuery, _Mapping]] = ..., upload_begin: _Optional[_Union[UploadBegin, _Mapping]] = ..., upload_chunk: _Optional[_Union[UploadChunk, _Mapping]] = ..., upload_finish: _Optional[_Union[UploadFinish, _Mapping]] = ..., file_get: _Optional[_Union[FileGet, _Mapping]] = ..., list_devices: _Optional[_Union[ListDevices, _Mapping]] = ..., revoke_device: _Optional[_Union[SignedDeviceRevocation, _Mapping]] = ..., publish_contact_card: _Optional[_Union[PublishContactCard, _Mapping]] = ..., get_contact_card: _Optional[_Union[GetContactCard, _Mapping]] = ..., voice_join: _Optional[_Union[VoiceJoin, _Mapping]] = ..., voice_state: _Optional[_Union[VoiceState, _Mapping]] = ..., voice_leave: _Optional[_Union[VoiceLeave, _Mapping]] = ..., check_status: _Optional[_Union[CheckStatus, _Mapping]] = ..., add_key_packages: _Optional[_Union[AddKeyPackages, _Mapping]] = ..., get_avatar: _Optional[_Union[GetAvatar, _Mapping]] = ..., adopt_server: _Optional[_Union[AdoptServer, _Mapping]] = ..., react: _Optional[_Union[React, _Mapping]] = ..., pin: _Optional[_Union[Pin, _Mapping]] = ..., list_pins: _Optional[_Union[ListPins, _Mapping]] = ..., typing: _Optional[_Union[Typing, _Mapping]] = ..., update_profile: _Optional[_Union[UpdateProfile, _Mapping]] = ..., report_message: _Optional[_Union[ReportMessage, _Mapping]] = ..., list_reports: _Optional[_Union[ListReports, _Mapping]] = ..., list_automod_rules: _Optional[_Union[ListAutoModRules, _Mapping]] = ..., get_emoji: _Optional[_Union[GetEmoji, _Mapping]] = ..., emoji_upload: _Optional[_Union[EmojiUpload, _Mapping]] = ..., set_status_device: _Optional[_Union[SetStatusDevice, _Mapping]] = ..., set_status: _Optional[_Union[SetStatus, _Mapping]] = ..., list_statuses: _Optional[_Union[ListStatuses, _Mapping]] = ..., send_status_keys: _Optional[_Union[SendStatusKeys, _Mapping]] = ..., fetch_status_keys: _Optional[_Union[FetchStatusKeys, _Mapping]] = ..., request_status_key: _Optional[_Union[RequestStatusKey, _Mapping]] = ...) -> None: ...

class ServerMessage(_message.Message):
    __slots__ = ("reply_to", "error", "ok", "info", "claim_result", "join_result", "state", "messages", "message", "admin_result", "members", "join_keys", "bans", "audit", "search_results", "upload_started", "file_chunk", "devices", "contact_card", "voice_session", "status", "avatar", "reports", "automod_rules", "emoji_image", "state_changed", "voice_update", "voice_rooms", "pins", "presence", "staged_file", "message_update", "mod_alert", "presence_batch", "mod_queue", "statuses", "status_keys", "status_key_request")
    REPLY_TO_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    OK_FIELD_NUMBER: _ClassVar[int]
    INFO_FIELD_NUMBER: _ClassVar[int]
    CLAIM_RESULT_FIELD_NUMBER: _ClassVar[int]
    JOIN_RESULT_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    MESSAGES_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    ADMIN_RESULT_FIELD_NUMBER: _ClassVar[int]
    MEMBERS_FIELD_NUMBER: _ClassVar[int]
    JOIN_KEYS_FIELD_NUMBER: _ClassVar[int]
    BANS_FIELD_NUMBER: _ClassVar[int]
    AUDIT_FIELD_NUMBER: _ClassVar[int]
    SEARCH_RESULTS_FIELD_NUMBER: _ClassVar[int]
    UPLOAD_STARTED_FIELD_NUMBER: _ClassVar[int]
    FILE_CHUNK_FIELD_NUMBER: _ClassVar[int]
    DEVICES_FIELD_NUMBER: _ClassVar[int]
    CONTACT_CARD_FIELD_NUMBER: _ClassVar[int]
    VOICE_SESSION_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    AVATAR_FIELD_NUMBER: _ClassVar[int]
    REPORTS_FIELD_NUMBER: _ClassVar[int]
    AUTOMOD_RULES_FIELD_NUMBER: _ClassVar[int]
    EMOJI_IMAGE_FIELD_NUMBER: _ClassVar[int]
    STATE_CHANGED_FIELD_NUMBER: _ClassVar[int]
    VOICE_UPDATE_FIELD_NUMBER: _ClassVar[int]
    VOICE_ROOMS_FIELD_NUMBER: _ClassVar[int]
    PINS_FIELD_NUMBER: _ClassVar[int]
    PRESENCE_FIELD_NUMBER: _ClassVar[int]
    STAGED_FILE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_UPDATE_FIELD_NUMBER: _ClassVar[int]
    MOD_ALERT_FIELD_NUMBER: _ClassVar[int]
    PRESENCE_BATCH_FIELD_NUMBER: _ClassVar[int]
    MOD_QUEUE_FIELD_NUMBER: _ClassVar[int]
    STATUSES_FIELD_NUMBER: _ClassVar[int]
    STATUS_KEYS_FIELD_NUMBER: _ClassVar[int]
    STATUS_KEY_REQUEST_FIELD_NUMBER: _ClassVar[int]
    reply_to: int
    error: RpcError
    ok: Ok
    info: CommunityInfo
    claim_result: ClaimResult
    join_result: JoinResult
    state: SyncState
    messages: MessageList
    message: Message
    admin_result: AdminResult
    members: MemberList
    join_keys: JoinKeyList
    bans: BanList
    audit: AuditList
    search_results: SearchResults
    upload_started: UploadStarted
    file_chunk: FileChunk
    devices: DeviceList
    contact_card: ContactCardResult
    voice_session: VoiceSession
    status: SessionStatus
    avatar: AvatarResult
    reports: ReportList
    automod_rules: AutoModRuleList
    emoji_image: EmojiImage
    state_changed: StateChanged
    voice_update: VoiceUpdate
    voice_rooms: VoiceRooms
    pins: PinList
    presence: PresenceUpdate
    staged_file: StagedFile
    message_update: MessageUpdate
    mod_alert: ModAlert
    presence_batch: PresenceBatch
    mod_queue: ModQueue
    statuses: StatusList
    status_keys: StatusKeys
    status_key_request: StatusKeyRequest
    def __init__(self, reply_to: _Optional[int] = ..., error: _Optional[_Union[RpcError, _Mapping]] = ..., ok: _Optional[_Union[Ok, _Mapping]] = ..., info: _Optional[_Union[CommunityInfo, _Mapping]] = ..., claim_result: _Optional[_Union[ClaimResult, _Mapping]] = ..., join_result: _Optional[_Union[JoinResult, _Mapping]] = ..., state: _Optional[_Union[SyncState, _Mapping]] = ..., messages: _Optional[_Union[MessageList, _Mapping]] = ..., message: _Optional[_Union[Message, _Mapping]] = ..., admin_result: _Optional[_Union[AdminResult, _Mapping]] = ..., members: _Optional[_Union[MemberList, _Mapping]] = ..., join_keys: _Optional[_Union[JoinKeyList, _Mapping]] = ..., bans: _Optional[_Union[BanList, _Mapping]] = ..., audit: _Optional[_Union[AuditList, _Mapping]] = ..., search_results: _Optional[_Union[SearchResults, _Mapping]] = ..., upload_started: _Optional[_Union[UploadStarted, _Mapping]] = ..., file_chunk: _Optional[_Union[FileChunk, _Mapping]] = ..., devices: _Optional[_Union[DeviceList, _Mapping]] = ..., contact_card: _Optional[_Union[ContactCardResult, _Mapping]] = ..., voice_session: _Optional[_Union[VoiceSession, _Mapping]] = ..., status: _Optional[_Union[SessionStatus, _Mapping]] = ..., avatar: _Optional[_Union[AvatarResult, _Mapping]] = ..., reports: _Optional[_Union[ReportList, _Mapping]] = ..., automod_rules: _Optional[_Union[AutoModRuleList, _Mapping]] = ..., emoji_image: _Optional[_Union[EmojiImage, _Mapping]] = ..., state_changed: _Optional[_Union[StateChanged, _Mapping]] = ..., voice_update: _Optional[_Union[VoiceUpdate, _Mapping]] = ..., voice_rooms: _Optional[_Union[VoiceRooms, _Mapping]] = ..., pins: _Optional[_Union[PinList, _Mapping]] = ..., presence: _Optional[_Union[PresenceUpdate, _Mapping]] = ..., staged_file: _Optional[_Union[StagedFile, _Mapping]] = ..., message_update: _Optional[_Union[MessageUpdate, _Mapping]] = ..., mod_alert: _Optional[_Union[ModAlert, _Mapping]] = ..., presence_batch: _Optional[_Union[PresenceBatch, _Mapping]] = ..., mod_queue: _Optional[_Union[ModQueue, _Mapping]] = ..., statuses: _Optional[_Union[StatusList, _Mapping]] = ..., status_keys: _Optional[_Union[StatusKeys, _Mapping]] = ..., status_key_request: _Optional[_Union[StatusKeyRequest, _Mapping]] = ...) -> None: ...

class Ok(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class RpcError(_message.Message):
    __slots__ = ("code", "message")
    CODE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    code: RpcErrorCode
    message: str
    def __init__(self, code: _Optional[_Union[RpcErrorCode, str]] = ..., message: _Optional[str] = ...) -> None: ...

class SessionStatus(_message.Message):
    __slots__ = ("status", "claimed")
    STATUS_FIELD_NUMBER: _ClassVar[int]
    CLAIMED_FIELD_NUMBER: _ClassVar[int]
    status: MemberStatus
    claimed: bool
    def __init__(self, status: _Optional[_Union[MemberStatus, str]] = ..., claimed: _Optional[bool] = ...) -> None: ...

class CommunitySettings(_message.Message):
    __slots__ = ("name", "description", "icon", "language", "tags", "visibility", "join_policy", "audit_visibility", "message_retention_days", "file_retention_days", "search_enabled", "max_file_size", "file_bandwidth_limit", "relay_mode", "pinned_relays", "max_devices_per_member", "allow_contact_cards", "max_members", "attachments_enabled", "attachment_kinds", "link_previews_enabled", "reactions_enabled", "custom_emoji_enabled", "voice_enabled", "video_enabled", "screen_share_enabled", "edit_own_enabled", "delete_own_enabled", "typing_enabled", "new_members_see_history", "join_leave_messages", "join_leave_channel", "screening_minutes", "max_custom_emoji", "lockdown", "new_member_minutes")
    NAME_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    ICON_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    TAGS_FIELD_NUMBER: _ClassVar[int]
    VISIBILITY_FIELD_NUMBER: _ClassVar[int]
    JOIN_POLICY_FIELD_NUMBER: _ClassVar[int]
    AUDIT_VISIBILITY_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_RETENTION_DAYS_FIELD_NUMBER: _ClassVar[int]
    FILE_RETENTION_DAYS_FIELD_NUMBER: _ClassVar[int]
    SEARCH_ENABLED_FIELD_NUMBER: _ClassVar[int]
    MAX_FILE_SIZE_FIELD_NUMBER: _ClassVar[int]
    FILE_BANDWIDTH_LIMIT_FIELD_NUMBER: _ClassVar[int]
    RELAY_MODE_FIELD_NUMBER: _ClassVar[int]
    PINNED_RELAYS_FIELD_NUMBER: _ClassVar[int]
    MAX_DEVICES_PER_MEMBER_FIELD_NUMBER: _ClassVar[int]
    ALLOW_CONTACT_CARDS_FIELD_NUMBER: _ClassVar[int]
    MAX_MEMBERS_FIELD_NUMBER: _ClassVar[int]
    ATTACHMENTS_ENABLED_FIELD_NUMBER: _ClassVar[int]
    ATTACHMENT_KINDS_FIELD_NUMBER: _ClassVar[int]
    LINK_PREVIEWS_ENABLED_FIELD_NUMBER: _ClassVar[int]
    REACTIONS_ENABLED_FIELD_NUMBER: _ClassVar[int]
    CUSTOM_EMOJI_ENABLED_FIELD_NUMBER: _ClassVar[int]
    VOICE_ENABLED_FIELD_NUMBER: _ClassVar[int]
    VIDEO_ENABLED_FIELD_NUMBER: _ClassVar[int]
    SCREEN_SHARE_ENABLED_FIELD_NUMBER: _ClassVar[int]
    EDIT_OWN_ENABLED_FIELD_NUMBER: _ClassVar[int]
    DELETE_OWN_ENABLED_FIELD_NUMBER: _ClassVar[int]
    TYPING_ENABLED_FIELD_NUMBER: _ClassVar[int]
    NEW_MEMBERS_SEE_HISTORY_FIELD_NUMBER: _ClassVar[int]
    JOIN_LEAVE_MESSAGES_FIELD_NUMBER: _ClassVar[int]
    JOIN_LEAVE_CHANNEL_FIELD_NUMBER: _ClassVar[int]
    SCREENING_MINUTES_FIELD_NUMBER: _ClassVar[int]
    MAX_CUSTOM_EMOJI_FIELD_NUMBER: _ClassVar[int]
    LOCKDOWN_FIELD_NUMBER: _ClassVar[int]
    NEW_MEMBER_MINUTES_FIELD_NUMBER: _ClassVar[int]
    name: str
    description: str
    icon: bytes
    language: str
    tags: _containers.RepeatedScalarFieldContainer[str]
    visibility: Visibility
    join_policy: JoinPolicy
    audit_visibility: AuditVisibility
    message_retention_days: int
    file_retention_days: int
    search_enabled: bool
    max_file_size: int
    file_bandwidth_limit: int
    relay_mode: RelayMode
    pinned_relays: _containers.RepeatedScalarFieldContainer[str]
    max_devices_per_member: int
    allow_contact_cards: bool
    max_members: int
    attachments_enabled: bool
    attachment_kinds: AttachmentKinds
    link_previews_enabled: bool
    reactions_enabled: bool
    custom_emoji_enabled: bool
    voice_enabled: bool
    video_enabled: bool
    screen_share_enabled: bool
    edit_own_enabled: bool
    delete_own_enabled: bool
    typing_enabled: bool
    new_members_see_history: bool
    join_leave_messages: bool
    join_leave_channel: int
    screening_minutes: int
    max_custom_emoji: int
    lockdown: bool
    new_member_minutes: int
    def __init__(self, name: _Optional[str] = ..., description: _Optional[str] = ..., icon: _Optional[bytes] = ..., language: _Optional[str] = ..., tags: _Optional[_Iterable[str]] = ..., visibility: _Optional[_Union[Visibility, str]] = ..., join_policy: _Optional[_Union[JoinPolicy, str]] = ..., audit_visibility: _Optional[_Union[AuditVisibility, str]] = ..., message_retention_days: _Optional[int] = ..., file_retention_days: _Optional[int] = ..., search_enabled: _Optional[bool] = ..., max_file_size: _Optional[int] = ..., file_bandwidth_limit: _Optional[int] = ..., relay_mode: _Optional[_Union[RelayMode, str]] = ..., pinned_relays: _Optional[_Iterable[str]] = ..., max_devices_per_member: _Optional[int] = ..., allow_contact_cards: _Optional[bool] = ..., max_members: _Optional[int] = ..., attachments_enabled: _Optional[bool] = ..., attachment_kinds: _Optional[_Union[AttachmentKinds, str]] = ..., link_previews_enabled: _Optional[bool] = ..., reactions_enabled: _Optional[bool] = ..., custom_emoji_enabled: _Optional[bool] = ..., voice_enabled: _Optional[bool] = ..., video_enabled: _Optional[bool] = ..., screen_share_enabled: _Optional[bool] = ..., edit_own_enabled: _Optional[bool] = ..., delete_own_enabled: _Optional[bool] = ..., typing_enabled: _Optional[bool] = ..., new_members_see_history: _Optional[bool] = ..., join_leave_messages: _Optional[bool] = ..., join_leave_channel: _Optional[int] = ..., screening_minutes: _Optional[int] = ..., max_custom_emoji: _Optional[int] = ..., lockdown: _Optional[bool] = ..., new_member_minutes: _Optional[int] = ...) -> None: ...

class Channel(_message.Message):
    __slots__ = ("id", "name", "kind", "position", "topic", "key", "key_epoch", "permissions", "message_retention_days", "file_retention_days", "overrides", "description", "category_id", "permissions_synced", "slowmode_seconds", "attachments", "link_previews", "reactions", "history_for_new_members")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    POSITION_FIELD_NUMBER: _ClassVar[int]
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    KEY_FIELD_NUMBER: _ClassVar[int]
    KEY_EPOCH_FIELD_NUMBER: _ClassVar[int]
    PERMISSIONS_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_RETENTION_DAYS_FIELD_NUMBER: _ClassVar[int]
    FILE_RETENTION_DAYS_FIELD_NUMBER: _ClassVar[int]
    OVERRIDES_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    CATEGORY_ID_FIELD_NUMBER: _ClassVar[int]
    PERMISSIONS_SYNCED_FIELD_NUMBER: _ClassVar[int]
    SLOWMODE_SECONDS_FIELD_NUMBER: _ClassVar[int]
    ATTACHMENTS_FIELD_NUMBER: _ClassVar[int]
    LINK_PREVIEWS_FIELD_NUMBER: _ClassVar[int]
    REACTIONS_FIELD_NUMBER: _ClassVar[int]
    HISTORY_FOR_NEW_MEMBERS_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    kind: ChannelKind
    position: int
    topic: bytes
    key: bytes
    key_epoch: int
    permissions: int
    message_retention_days: int
    file_retention_days: int
    overrides: _containers.RepeatedCompositeFieldContainer[ChannelOverride]
    description: str
    category_id: int
    permissions_synced: bool
    slowmode_seconds: int
    attachments: ChannelToggle
    link_previews: ChannelToggle
    reactions: ChannelToggle
    history_for_new_members: ChannelToggle
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ..., kind: _Optional[_Union[ChannelKind, str]] = ..., position: _Optional[int] = ..., topic: _Optional[bytes] = ..., key: _Optional[bytes] = ..., key_epoch: _Optional[int] = ..., permissions: _Optional[int] = ..., message_retention_days: _Optional[int] = ..., file_retention_days: _Optional[int] = ..., overrides: _Optional[_Iterable[_Union[ChannelOverride, _Mapping]]] = ..., description: _Optional[str] = ..., category_id: _Optional[int] = ..., permissions_synced: _Optional[bool] = ..., slowmode_seconds: _Optional[int] = ..., attachments: _Optional[_Union[ChannelToggle, str]] = ..., link_previews: _Optional[_Union[ChannelToggle, str]] = ..., reactions: _Optional[_Union[ChannelToggle, str]] = ..., history_for_new_members: _Optional[_Union[ChannelToggle, str]] = ...) -> None: ...

class Category(_message.Message):
    __slots__ = ("id", "name", "position", "overrides")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    POSITION_FIELD_NUMBER: _ClassVar[int]
    OVERRIDES_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    position: int
    overrides: _containers.RepeatedCompositeFieldContainer[ChannelOverride]
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ..., position: _Optional[int] = ..., overrides: _Optional[_Iterable[_Union[ChannelOverride, _Mapping]]] = ...) -> None: ...

class ChannelOverride(_message.Message):
    __slots__ = ("role_id", "allow", "deny", "member")
    ROLE_ID_FIELD_NUMBER: _ClassVar[int]
    ALLOW_FIELD_NUMBER: _ClassVar[int]
    DENY_FIELD_NUMBER: _ClassVar[int]
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    role_id: int
    allow: int
    deny: int
    member: bytes
    def __init__(self, role_id: _Optional[int] = ..., allow: _Optional[int] = ..., deny: _Optional[int] = ..., member: _Optional[bytes] = ...) -> None: ...

class Role(_message.Message):
    __slots__ = ("id", "name", "permissions", "position", "color", "builtin", "hoist", "mentionable")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    PERMISSIONS_FIELD_NUMBER: _ClassVar[int]
    POSITION_FIELD_NUMBER: _ClassVar[int]
    COLOR_FIELD_NUMBER: _ClassVar[int]
    BUILTIN_FIELD_NUMBER: _ClassVar[int]
    HOIST_FIELD_NUMBER: _ClassVar[int]
    MENTIONABLE_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    permissions: int
    position: int
    color: int
    builtin: bool
    hoist: bool
    mentionable: bool
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ..., permissions: _Optional[int] = ..., position: _Optional[int] = ..., color: _Optional[int] = ..., builtin: _Optional[bool] = ..., hoist: _Optional[bool] = ..., mentionable: _Optional[bool] = ...) -> None: ...

class Member(_message.Message):
    __slots__ = ("key", "display_name", "has_avatar", "role_ids", "joined_at", "join_key_id", "status", "owner", "avatar_version", "owner_invited", "voice_muted", "voice_deafened", "nickname", "about", "pronouns", "online", "timeout_until", "can_post_at")
    KEY_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    HAS_AVATAR_FIELD_NUMBER: _ClassVar[int]
    ROLE_IDS_FIELD_NUMBER: _ClassVar[int]
    JOINED_AT_FIELD_NUMBER: _ClassVar[int]
    JOIN_KEY_ID_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    OWNER_FIELD_NUMBER: _ClassVar[int]
    AVATAR_VERSION_FIELD_NUMBER: _ClassVar[int]
    OWNER_INVITED_FIELD_NUMBER: _ClassVar[int]
    VOICE_MUTED_FIELD_NUMBER: _ClassVar[int]
    VOICE_DEAFENED_FIELD_NUMBER: _ClassVar[int]
    NICKNAME_FIELD_NUMBER: _ClassVar[int]
    ABOUT_FIELD_NUMBER: _ClassVar[int]
    PRONOUNS_FIELD_NUMBER: _ClassVar[int]
    ONLINE_FIELD_NUMBER: _ClassVar[int]
    TIMEOUT_UNTIL_FIELD_NUMBER: _ClassVar[int]
    CAN_POST_AT_FIELD_NUMBER: _ClassVar[int]
    key: bytes
    display_name: str
    has_avatar: bool
    role_ids: _containers.RepeatedScalarFieldContainer[int]
    joined_at: int
    join_key_id: int
    status: MemberStatus
    owner: bool
    avatar_version: int
    owner_invited: bool
    voice_muted: bool
    voice_deafened: bool
    nickname: str
    about: str
    pronouns: str
    online: bool
    timeout_until: int
    can_post_at: int
    def __init__(self, key: _Optional[bytes] = ..., display_name: _Optional[str] = ..., has_avatar: _Optional[bool] = ..., role_ids: _Optional[_Iterable[int]] = ..., joined_at: _Optional[int] = ..., join_key_id: _Optional[int] = ..., status: _Optional[_Union[MemberStatus, str]] = ..., owner: _Optional[bool] = ..., avatar_version: _Optional[int] = ..., owner_invited: _Optional[bool] = ..., voice_muted: _Optional[bool] = ..., voice_deafened: _Optional[bool] = ..., nickname: _Optional[str] = ..., about: _Optional[str] = ..., pronouns: _Optional[str] = ..., online: _Optional[bool] = ..., timeout_until: _Optional[int] = ..., can_post_at: _Optional[int] = ...) -> None: ...

class FileRef(_message.Message):
    __slots__ = ("file_id", "name", "size", "mime", "key", "chunk_size")
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    MIME_FIELD_NUMBER: _ClassVar[int]
    KEY_FIELD_NUMBER: _ClassVar[int]
    CHUNK_SIZE_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    name: str
    size: int
    mime: str
    key: bytes
    chunk_size: int
    def __init__(self, file_id: _Optional[int] = ..., name: _Optional[str] = ..., size: _Optional[int] = ..., mime: _Optional[str] = ..., key: _Optional[bytes] = ..., chunk_size: _Optional[int] = ...) -> None: ...

class Message(_message.Message):
    __slots__ = ("id", "channel_id", "author", "author_name", "text", "created_at", "edited_at", "deleted", "reply_to", "mentions", "files", "mention_roles", "mention_everyone", "mention_here", "reactions", "pinned", "preview", "system")
    ID_FIELD_NUMBER: _ClassVar[int]
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    AUTHOR_FIELD_NUMBER: _ClassVar[int]
    AUTHOR_NAME_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    EDITED_AT_FIELD_NUMBER: _ClassVar[int]
    DELETED_FIELD_NUMBER: _ClassVar[int]
    REPLY_TO_FIELD_NUMBER: _ClassVar[int]
    MENTIONS_FIELD_NUMBER: _ClassVar[int]
    FILES_FIELD_NUMBER: _ClassVar[int]
    MENTION_ROLES_FIELD_NUMBER: _ClassVar[int]
    MENTION_EVERYONE_FIELD_NUMBER: _ClassVar[int]
    MENTION_HERE_FIELD_NUMBER: _ClassVar[int]
    REACTIONS_FIELD_NUMBER: _ClassVar[int]
    PINNED_FIELD_NUMBER: _ClassVar[int]
    PREVIEW_FIELD_NUMBER: _ClassVar[int]
    SYSTEM_FIELD_NUMBER: _ClassVar[int]
    id: int
    channel_id: int
    author: bytes
    author_name: str
    text: str
    created_at: int
    edited_at: int
    deleted: bool
    reply_to: int
    mentions: _containers.RepeatedScalarFieldContainer[bytes]
    files: _containers.RepeatedCompositeFieldContainer[FileRef]
    mention_roles: _containers.RepeatedScalarFieldContainer[int]
    mention_everyone: bool
    mention_here: bool
    reactions: _containers.RepeatedCompositeFieldContainer[Reaction]
    pinned: bool
    preview: LinkPreview
    system: SystemEvent
    def __init__(self, id: _Optional[int] = ..., channel_id: _Optional[int] = ..., author: _Optional[bytes] = ..., author_name: _Optional[str] = ..., text: _Optional[str] = ..., created_at: _Optional[int] = ..., edited_at: _Optional[int] = ..., deleted: _Optional[bool] = ..., reply_to: _Optional[int] = ..., mentions: _Optional[_Iterable[bytes]] = ..., files: _Optional[_Iterable[_Union[FileRef, _Mapping]]] = ..., mention_roles: _Optional[_Iterable[int]] = ..., mention_everyone: _Optional[bool] = ..., mention_here: _Optional[bool] = ..., reactions: _Optional[_Iterable[_Union[Reaction, _Mapping]]] = ..., pinned: _Optional[bool] = ..., preview: _Optional[_Union[LinkPreview, _Mapping]] = ..., system: _Optional[_Union[SystemEvent, _Mapping]] = ...) -> None: ...

class Reaction(_message.Message):
    __slots__ = ("emoji", "members")
    EMOJI_FIELD_NUMBER: _ClassVar[int]
    MEMBERS_FIELD_NUMBER: _ClassVar[int]
    emoji: str
    members: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, emoji: _Optional[str] = ..., members: _Optional[_Iterable[bytes]] = ...) -> None: ...

class LinkPreview(_message.Message):
    __slots__ = ("url", "title", "description", "site_name", "image")
    URL_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    SITE_NAME_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    url: str
    title: str
    description: str
    site_name: str
    image: FileRef
    def __init__(self, url: _Optional[str] = ..., title: _Optional[str] = ..., description: _Optional[str] = ..., site_name: _Optional[str] = ..., image: _Optional[_Union[FileRef, _Mapping]] = ...) -> None: ...

class SystemEvent(_message.Message):
    __slots__ = ("kind", "member")
    KIND_FIELD_NUMBER: _ClassVar[int]
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    kind: SystemEventKind
    member: bytes
    def __init__(self, kind: _Optional[_Union[SystemEventKind, str]] = ..., member: _Optional[bytes] = ...) -> None: ...

class JoinKey(_message.Message):
    __slots__ = ("id", "label", "secret", "created_by", "created_at", "expires_at", "max_uses", "uses", "role_ids", "request_only", "revoked", "public", "created_by_name")
    ID_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    SECRET_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    MAX_USES_FIELD_NUMBER: _ClassVar[int]
    USES_FIELD_NUMBER: _ClassVar[int]
    ROLE_IDS_FIELD_NUMBER: _ClassVar[int]
    REQUEST_ONLY_FIELD_NUMBER: _ClassVar[int]
    REVOKED_FIELD_NUMBER: _ClassVar[int]
    PUBLIC_FIELD_NUMBER: _ClassVar[int]
    CREATED_BY_NAME_FIELD_NUMBER: _ClassVar[int]
    id: int
    label: str
    secret: str
    created_by: bytes
    created_at: int
    expires_at: int
    max_uses: int
    uses: int
    role_ids: _containers.RepeatedScalarFieldContainer[int]
    request_only: bool
    revoked: bool
    public: bool
    created_by_name: str
    def __init__(self, id: _Optional[int] = ..., label: _Optional[str] = ..., secret: _Optional[str] = ..., created_by: _Optional[bytes] = ..., created_at: _Optional[int] = ..., expires_at: _Optional[int] = ..., max_uses: _Optional[int] = ..., uses: _Optional[int] = ..., role_ids: _Optional[_Iterable[int]] = ..., request_only: _Optional[bool] = ..., revoked: _Optional[bool] = ..., public: _Optional[bool] = ..., created_by_name: _Optional[str] = ...) -> None: ...

class GetInfo(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class CommunityInfo(_message.Message):
    __slots__ = ("owner_key", "server_key", "name", "description", "member_range", "join_policy", "visibility", "icon", "tags", "language", "claimed")
    OWNER_KEY_FIELD_NUMBER: _ClassVar[int]
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    MEMBER_RANGE_FIELD_NUMBER: _ClassVar[int]
    JOIN_POLICY_FIELD_NUMBER: _ClassVar[int]
    VISIBILITY_FIELD_NUMBER: _ClassVar[int]
    ICON_FIELD_NUMBER: _ClassVar[int]
    TAGS_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    CLAIMED_FIELD_NUMBER: _ClassVar[int]
    owner_key: bytes
    server_key: bytes
    name: str
    description: str
    member_range: str
    join_policy: JoinPolicy
    visibility: Visibility
    icon: bytes
    tags: _containers.RepeatedScalarFieldContainer[str]
    language: str
    claimed: bool
    def __init__(self, owner_key: _Optional[bytes] = ..., server_key: _Optional[bytes] = ..., name: _Optional[str] = ..., description: _Optional[str] = ..., member_range: _Optional[str] = ..., join_policy: _Optional[_Union[JoinPolicy, str]] = ..., visibility: _Optional[_Union[Visibility, str]] = ..., icon: _Optional[bytes] = ..., tags: _Optional[_Iterable[str]] = ..., language: _Optional[str] = ..., claimed: _Optional[bool] = ...) -> None: ...

class Claim(_message.Message):
    __slots__ = ("secret", "cert", "settings", "display_name")
    SECRET_FIELD_NUMBER: _ClassVar[int]
    CERT_FIELD_NUMBER: _ClassVar[int]
    SETTINGS_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    secret: bytes
    cert: _session_pb2.SignedServerCert
    settings: CommunitySettings
    display_name: str
    def __init__(self, secret: _Optional[bytes] = ..., cert: _Optional[_Union[_session_pb2.SignedServerCert, _Mapping]] = ..., settings: _Optional[_Union[CommunitySettings, _Mapping]] = ..., display_name: _Optional[str] = ...) -> None: ...

class ClaimResult(_message.Message):
    __slots__ = ("state", "first_invite")
    STATE_FIELD_NUMBER: _ClassVar[int]
    FIRST_INVITE_FIELD_NUMBER: _ClassVar[int]
    state: SyncState
    first_invite: JoinKey
    def __init__(self, state: _Optional[_Union[SyncState, _Mapping]] = ..., first_invite: _Optional[_Union[JoinKey, _Mapping]] = ...) -> None: ...

class Join(_message.Message):
    __slots__ = ("join_key", "display_name")
    JOIN_KEY_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    join_key: str
    display_name: str
    def __init__(self, join_key: _Optional[str] = ..., display_name: _Optional[str] = ...) -> None: ...

class JoinResult(_message.Message):
    __slots__ = ("status", "reason", "state")
    STATUS_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    status: JoinStatus
    reason: RefusalReason
    state: SyncState
    def __init__(self, status: _Optional[_Union[JoinStatus, str]] = ..., reason: _Optional[_Union[RefusalReason, str]] = ..., state: _Optional[_Union[SyncState, _Mapping]] = ...) -> None: ...

class CheckStatus(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class Sync(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class SyncState(_message.Message):
    __slots__ = ("info", "self", "roles", "channels", "settings", "permissions", "cert", "owners", "pending_owners", "categories", "emoji", "dedicated_relay", "mod_queue")
    INFO_FIELD_NUMBER: _ClassVar[int]
    SELF_FIELD_NUMBER: _ClassVar[int]
    ROLES_FIELD_NUMBER: _ClassVar[int]
    CHANNELS_FIELD_NUMBER: _ClassVar[int]
    SETTINGS_FIELD_NUMBER: _ClassVar[int]
    PERMISSIONS_FIELD_NUMBER: _ClassVar[int]
    CERT_FIELD_NUMBER: _ClassVar[int]
    OWNERS_FIELD_NUMBER: _ClassVar[int]
    PENDING_OWNERS_FIELD_NUMBER: _ClassVar[int]
    CATEGORIES_FIELD_NUMBER: _ClassVar[int]
    EMOJI_FIELD_NUMBER: _ClassVar[int]
    DEDICATED_RELAY_FIELD_NUMBER: _ClassVar[int]
    MOD_QUEUE_FIELD_NUMBER: _ClassVar[int]
    info: CommunityInfo
    self: Member
    roles: _containers.RepeatedCompositeFieldContainer[Role]
    channels: _containers.RepeatedCompositeFieldContainer[Channel]
    settings: CommunitySettings
    permissions: int
    cert: _session_pb2.SignedServerCert
    owners: _containers.RepeatedCompositeFieldContainer[OwnerEntry]
    pending_owners: _containers.RepeatedCompositeFieldContainer[PendingOwner]
    categories: _containers.RepeatedCompositeFieldContainer[Category]
    emoji: _containers.RepeatedCompositeFieldContainer[CustomEmoji]
    dedicated_relay: bool
    mod_queue: ModQueue
    def __init__(self_, info: _Optional[_Union[CommunityInfo, _Mapping]] = ..., self: _Optional[_Union[Member, _Mapping]] = ..., roles: _Optional[_Iterable[_Union[Role, _Mapping]]] = ..., channels: _Optional[_Iterable[_Union[Channel, _Mapping]]] = ..., settings: _Optional[_Union[CommunitySettings, _Mapping]] = ..., permissions: _Optional[int] = ..., cert: _Optional[_Union[_session_pb2.SignedServerCert, _Mapping]] = ..., owners: _Optional[_Iterable[_Union[OwnerEntry, _Mapping]]] = ..., pending_owners: _Optional[_Iterable[_Union[PendingOwner, _Mapping]]] = ..., categories: _Optional[_Iterable[_Union[Category, _Mapping]]] = ..., emoji: _Optional[_Iterable[_Union[CustomEmoji, _Mapping]]] = ..., dedicated_relay: _Optional[bool] = ..., mod_queue: _Optional[_Union[ModQueue, _Mapping]] = ...) -> None: ...

class OwnerEntry(_message.Message):
    __slots__ = ("owner_key", "member", "display_name", "leaving")
    OWNER_KEY_FIELD_NUMBER: _ClassVar[int]
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    LEAVING_FIELD_NUMBER: _ClassVar[int]
    owner_key: bytes
    member: bytes
    display_name: str
    leaving: bool
    def __init__(self, owner_key: _Optional[bytes] = ..., member: _Optional[bytes] = ..., display_name: _Optional[str] = ..., leaving: _Optional[bool] = ...) -> None: ...

class PendingOwner(_message.Message):
    __slots__ = ("member", "display_name", "add")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    ADD_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    display_name: str
    add: _session_pb2.OwnerAdd
    def __init__(self, member: _Optional[bytes] = ..., display_name: _Optional[str] = ..., add: _Optional[_Union[_session_pb2.OwnerAdd, _Mapping]] = ...) -> None: ...

class StateChanged(_message.Message):
    __slots__ = ("state", "status")
    STATE_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    state: SyncState
    status: MemberStatus
    def __init__(self, state: _Optional[_Union[SyncState, _Mapping]] = ..., status: _Optional[_Union[MemberStatus, str]] = ...) -> None: ...

class History(_message.Message):
    __slots__ = ("channel_id", "before_id", "after_id", "limit")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    BEFORE_ID_FIELD_NUMBER: _ClassVar[int]
    AFTER_ID_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    before_id: int
    after_id: int
    limit: int
    def __init__(self, channel_id: _Optional[int] = ..., before_id: _Optional[int] = ..., after_id: _Optional[int] = ..., limit: _Optional[int] = ...) -> None: ...

class MessageList(_message.Message):
    __slots__ = ("messages", "more")
    MESSAGES_FIELD_NUMBER: _ClassVar[int]
    MORE_FIELD_NUMBER: _ClassVar[int]
    messages: _containers.RepeatedCompositeFieldContainer[Message]
    more: bool
    def __init__(self, messages: _Optional[_Iterable[_Union[Message, _Mapping]]] = ..., more: _Optional[bool] = ...) -> None: ...

class Post(_message.Message):
    __slots__ = ("channel_id", "text", "reply_to", "file_ids")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    REPLY_TO_FIELD_NUMBER: _ClassVar[int]
    FILE_IDS_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    text: str
    reply_to: int
    file_ids: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, channel_id: _Optional[int] = ..., text: _Optional[str] = ..., reply_to: _Optional[int] = ..., file_ids: _Optional[_Iterable[int]] = ...) -> None: ...

class Edit(_message.Message):
    __slots__ = ("message_id", "text")
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    message_id: int
    text: str
    def __init__(self, message_id: _Optional[int] = ..., text: _Optional[str] = ...) -> None: ...

class DeleteMessage(_message.Message):
    __slots__ = ("message_id",)
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    message_id: int
    def __init__(self, message_id: _Optional[int] = ...) -> None: ...

class Leave(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class SetProfile(_message.Message):
    __slots__ = ("display_name", "avatar", "clear_avatar")
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    AVATAR_FIELD_NUMBER: _ClassVar[int]
    CLEAR_AVATAR_FIELD_NUMBER: _ClassVar[int]
    display_name: str
    avatar: bytes
    clear_avatar: bool
    def __init__(self, display_name: _Optional[str] = ..., avatar: _Optional[bytes] = ..., clear_avatar: _Optional[bool] = ...) -> None: ...

class GetAvatar(_message.Message):
    __slots__ = ("member",)
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    def __init__(self, member: _Optional[bytes] = ...) -> None: ...

class AvatarResult(_message.Message):
    __slots__ = ("member", "avatar", "version")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    AVATAR_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    avatar: bytes
    version: int
    def __init__(self, member: _Optional[bytes] = ..., avatar: _Optional[bytes] = ..., version: _Optional[int] = ...) -> None: ...

class ListMembers(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class MemberList(_message.Message):
    __slots__ = ("members",)
    MEMBERS_FIELD_NUMBER: _ClassVar[int]
    members: _containers.RepeatedCompositeFieldContainer[Member]
    def __init__(self, members: _Optional[_Iterable[_Union[Member, _Mapping]]] = ...) -> None: ...

class ListJoinKeys(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class JoinKeyList(_message.Message):
    __slots__ = ("keys",)
    KEYS_FIELD_NUMBER: _ClassVar[int]
    keys: _containers.RepeatedCompositeFieldContainer[JoinKey]
    def __init__(self, keys: _Optional[_Iterable[_Union[JoinKey, _Mapping]]] = ...) -> None: ...

class ListPending(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ListBans(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class Ban(_message.Message):
    __slots__ = ("key", "display_name", "reason", "banned_by", "at", "network_block", "network_block_until")
    KEY_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    BANNED_BY_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    NETWORK_BLOCK_FIELD_NUMBER: _ClassVar[int]
    NETWORK_BLOCK_UNTIL_FIELD_NUMBER: _ClassVar[int]
    key: bytes
    display_name: str
    reason: str
    banned_by: bytes
    at: int
    network_block: NetworkBlockStatus
    network_block_until: int
    def __init__(self, key: _Optional[bytes] = ..., display_name: _Optional[str] = ..., reason: _Optional[str] = ..., banned_by: _Optional[bytes] = ..., at: _Optional[int] = ..., network_block: _Optional[_Union[NetworkBlockStatus, str]] = ..., network_block_until: _Optional[int] = ...) -> None: ...

class BanList(_message.Message):
    __slots__ = ("bans",)
    BANS_FIELD_NUMBER: _ClassVar[int]
    bans: _containers.RepeatedCompositeFieldContainer[Ban]
    def __init__(self, bans: _Optional[_Iterable[_Union[Ban, _Mapping]]] = ...) -> None: ...

class AuditQuery(_message.Message):
    __slots__ = ("before_id", "limit", "action", "actor", "target", "since", "until")
    BEFORE_ID_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    ACTION_FIELD_NUMBER: _ClassVar[int]
    ACTOR_FIELD_NUMBER: _ClassVar[int]
    TARGET_FIELD_NUMBER: _ClassVar[int]
    SINCE_FIELD_NUMBER: _ClassVar[int]
    UNTIL_FIELD_NUMBER: _ClassVar[int]
    before_id: int
    limit: int
    action: str
    actor: bytes
    target: bytes
    since: int
    until: int
    def __init__(self, before_id: _Optional[int] = ..., limit: _Optional[int] = ..., action: _Optional[str] = ..., actor: _Optional[bytes] = ..., target: _Optional[bytes] = ..., since: _Optional[int] = ..., until: _Optional[int] = ...) -> None: ...

class AuditEntry(_message.Message):
    __slots__ = ("id", "at", "actor", "actor_name", "summary", "action", "action_name", "target", "target_name", "system")
    ID_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    ACTOR_FIELD_NUMBER: _ClassVar[int]
    ACTOR_NAME_FIELD_NUMBER: _ClassVar[int]
    SUMMARY_FIELD_NUMBER: _ClassVar[int]
    ACTION_FIELD_NUMBER: _ClassVar[int]
    ACTION_NAME_FIELD_NUMBER: _ClassVar[int]
    TARGET_FIELD_NUMBER: _ClassVar[int]
    TARGET_NAME_FIELD_NUMBER: _ClassVar[int]
    SYSTEM_FIELD_NUMBER: _ClassVar[int]
    id: int
    at: int
    actor: bytes
    actor_name: str
    summary: str
    action: SignedAction
    action_name: str
    target: bytes
    target_name: str
    system: bool
    def __init__(self, id: _Optional[int] = ..., at: _Optional[int] = ..., actor: _Optional[bytes] = ..., actor_name: _Optional[str] = ..., summary: _Optional[str] = ..., action: _Optional[_Union[SignedAction, _Mapping]] = ..., action_name: _Optional[str] = ..., target: _Optional[bytes] = ..., target_name: _Optional[str] = ..., system: _Optional[bool] = ...) -> None: ...

class AuditList(_message.Message):
    __slots__ = ("entries", "more", "next_before_id")
    ENTRIES_FIELD_NUMBER: _ClassVar[int]
    MORE_FIELD_NUMBER: _ClassVar[int]
    NEXT_BEFORE_ID_FIELD_NUMBER: _ClassVar[int]
    entries: _containers.RepeatedCompositeFieldContainer[AuditEntry]
    more: bool
    next_before_id: int
    def __init__(self, entries: _Optional[_Iterable[_Union[AuditEntry, _Mapping]]] = ..., more: _Optional[bool] = ..., next_before_id: _Optional[int] = ...) -> None: ...

class SearchQuery(_message.Message):
    __slots__ = ("query", "channel_id", "limit")
    QUERY_FIELD_NUMBER: _ClassVar[int]
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    query: str
    channel_id: int
    limit: int
    def __init__(self, query: _Optional[str] = ..., channel_id: _Optional[int] = ..., limit: _Optional[int] = ...) -> None: ...

class SearchResults(_message.Message):
    __slots__ = ("messages",)
    MESSAGES_FIELD_NUMBER: _ClassVar[int]
    messages: _containers.RepeatedCompositeFieldContainer[Message]
    def __init__(self, messages: _Optional[_Iterable[_Union[Message, _Mapping]]] = ...) -> None: ...

class UploadBegin(_message.Message):
    __slots__ = ("channel_id", "name", "size", "mime", "text", "staged")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    MIME_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    STAGED_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    name: str
    size: int
    mime: str
    text: str
    staged: bool
    def __init__(self, channel_id: _Optional[int] = ..., name: _Optional[str] = ..., size: _Optional[int] = ..., mime: _Optional[str] = ..., text: _Optional[str] = ..., staged: _Optional[bool] = ...) -> None: ...

class StagedFile(_message.Message):
    __slots__ = ("file",)
    FILE_FIELD_NUMBER: _ClassVar[int]
    file: FileRef
    def __init__(self, file: _Optional[_Union[FileRef, _Mapping]] = ...) -> None: ...

class UploadStarted(_message.Message):
    __slots__ = ("upload_id", "chunk_size")
    UPLOAD_ID_FIELD_NUMBER: _ClassVar[int]
    CHUNK_SIZE_FIELD_NUMBER: _ClassVar[int]
    upload_id: int
    chunk_size: int
    def __init__(self, upload_id: _Optional[int] = ..., chunk_size: _Optional[int] = ...) -> None: ...

class UploadChunk(_message.Message):
    __slots__ = ("upload_id", "offset", "data")
    UPLOAD_ID_FIELD_NUMBER: _ClassVar[int]
    OFFSET_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    upload_id: int
    offset: int
    data: bytes
    def __init__(self, upload_id: _Optional[int] = ..., offset: _Optional[int] = ..., data: _Optional[bytes] = ...) -> None: ...

class UploadFinish(_message.Message):
    __slots__ = ("upload_id", "sha256")
    UPLOAD_ID_FIELD_NUMBER: _ClassVar[int]
    SHA256_FIELD_NUMBER: _ClassVar[int]
    upload_id: int
    sha256: bytes
    def __init__(self, upload_id: _Optional[int] = ..., sha256: _Optional[bytes] = ...) -> None: ...

class FileGet(_message.Message):
    __slots__ = ("file_id", "chunk")
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    CHUNK_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    chunk: int
    def __init__(self, file_id: _Optional[int] = ..., chunk: _Optional[int] = ...) -> None: ...

class FileChunk(_message.Message):
    __slots__ = ("file_id", "chunk", "chunks", "data")
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    CHUNK_FIELD_NUMBER: _ClassVar[int]
    CHUNKS_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    chunk: int
    chunks: int
    data: bytes
    def __init__(self, file_id: _Optional[int] = ..., chunk: _Optional[int] = ..., chunks: _Optional[int] = ..., data: _Optional[bytes] = ...) -> None: ...

class ListDevices(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class Device(_message.Message):
    __slots__ = ("device_key", "name", "first_seen", "last_seen", "revoked")
    DEVICE_KEY_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    FIRST_SEEN_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_FIELD_NUMBER: _ClassVar[int]
    REVOKED_FIELD_NUMBER: _ClassVar[int]
    device_key: bytes
    name: str
    first_seen: int
    last_seen: int
    revoked: bool
    def __init__(self, device_key: _Optional[bytes] = ..., name: _Optional[str] = ..., first_seen: _Optional[int] = ..., last_seen: _Optional[int] = ..., revoked: _Optional[bool] = ...) -> None: ...

class DeviceList(_message.Message):
    __slots__ = ("devices",)
    DEVICES_FIELD_NUMBER: _ClassVar[int]
    devices: _containers.RepeatedCompositeFieldContainer[Device]
    def __init__(self, devices: _Optional[_Iterable[_Union[Device, _Mapping]]] = ...) -> None: ...

class DeviceRevocation(_message.Message):
    __slots__ = ("community", "identity_key", "device_key", "at")
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    IDENTITY_KEY_FIELD_NUMBER: _ClassVar[int]
    DEVICE_KEY_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    community: bytes
    identity_key: bytes
    device_key: bytes
    at: int
    def __init__(self, community: _Optional[bytes] = ..., identity_key: _Optional[bytes] = ..., device_key: _Optional[bytes] = ..., at: _Optional[int] = ...) -> None: ...

class SignedDeviceRevocation(_message.Message):
    __slots__ = ("revocation", "signature")
    REVOCATION_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    revocation: bytes
    signature: bytes
    def __init__(self, revocation: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class SignedAction(_message.Message):
    __slots__ = ("action", "signature")
    ACTION_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    action: bytes
    signature: bytes
    def __init__(self, action: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class AdminAction(_message.Message):
    __slots__ = ("community", "actor", "at", "nonce", "update_settings", "create_join_key", "revoke_join_key", "create_channel", "edit_channel", "delete_channel", "create_role", "edit_role", "delete_role", "reorder_roles", "set_channel_override", "assign_roles", "kick", "ban", "unban", "approve", "deny", "raid_revoke", "replace_member", "prepare_key_rotation", "commit_key_rotation", "server_backup", "create_community", "invite_owner", "submit_owner_key", "change_owners", "create_category", "edit_category", "delete_category", "reorder_categories", "reorder_channels", "voice_mute", "voice_deafen", "voice_move", "set_nickname", "timeout", "bulk_delete", "save_automod_rule", "delete_automod_rule", "resolve_report", "add_emoji", "rename_emoji", "delete_emoji", "bulk_remove")
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    ACTOR_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    NONCE_FIELD_NUMBER: _ClassVar[int]
    UPDATE_SETTINGS_FIELD_NUMBER: _ClassVar[int]
    CREATE_JOIN_KEY_FIELD_NUMBER: _ClassVar[int]
    REVOKE_JOIN_KEY_FIELD_NUMBER: _ClassVar[int]
    CREATE_CHANNEL_FIELD_NUMBER: _ClassVar[int]
    EDIT_CHANNEL_FIELD_NUMBER: _ClassVar[int]
    DELETE_CHANNEL_FIELD_NUMBER: _ClassVar[int]
    CREATE_ROLE_FIELD_NUMBER: _ClassVar[int]
    EDIT_ROLE_FIELD_NUMBER: _ClassVar[int]
    DELETE_ROLE_FIELD_NUMBER: _ClassVar[int]
    REORDER_ROLES_FIELD_NUMBER: _ClassVar[int]
    SET_CHANNEL_OVERRIDE_FIELD_NUMBER: _ClassVar[int]
    ASSIGN_ROLES_FIELD_NUMBER: _ClassVar[int]
    KICK_FIELD_NUMBER: _ClassVar[int]
    BAN_FIELD_NUMBER: _ClassVar[int]
    UNBAN_FIELD_NUMBER: _ClassVar[int]
    APPROVE_FIELD_NUMBER: _ClassVar[int]
    DENY_FIELD_NUMBER: _ClassVar[int]
    RAID_REVOKE_FIELD_NUMBER: _ClassVar[int]
    REPLACE_MEMBER_FIELD_NUMBER: _ClassVar[int]
    PREPARE_KEY_ROTATION_FIELD_NUMBER: _ClassVar[int]
    COMMIT_KEY_ROTATION_FIELD_NUMBER: _ClassVar[int]
    SERVER_BACKUP_FIELD_NUMBER: _ClassVar[int]
    CREATE_COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    INVITE_OWNER_FIELD_NUMBER: _ClassVar[int]
    SUBMIT_OWNER_KEY_FIELD_NUMBER: _ClassVar[int]
    CHANGE_OWNERS_FIELD_NUMBER: _ClassVar[int]
    CREATE_CATEGORY_FIELD_NUMBER: _ClassVar[int]
    EDIT_CATEGORY_FIELD_NUMBER: _ClassVar[int]
    DELETE_CATEGORY_FIELD_NUMBER: _ClassVar[int]
    REORDER_CATEGORIES_FIELD_NUMBER: _ClassVar[int]
    REORDER_CHANNELS_FIELD_NUMBER: _ClassVar[int]
    VOICE_MUTE_FIELD_NUMBER: _ClassVar[int]
    VOICE_DEAFEN_FIELD_NUMBER: _ClassVar[int]
    VOICE_MOVE_FIELD_NUMBER: _ClassVar[int]
    SET_NICKNAME_FIELD_NUMBER: _ClassVar[int]
    TIMEOUT_FIELD_NUMBER: _ClassVar[int]
    BULK_DELETE_FIELD_NUMBER: _ClassVar[int]
    SAVE_AUTOMOD_RULE_FIELD_NUMBER: _ClassVar[int]
    DELETE_AUTOMOD_RULE_FIELD_NUMBER: _ClassVar[int]
    RESOLVE_REPORT_FIELD_NUMBER: _ClassVar[int]
    ADD_EMOJI_FIELD_NUMBER: _ClassVar[int]
    RENAME_EMOJI_FIELD_NUMBER: _ClassVar[int]
    DELETE_EMOJI_FIELD_NUMBER: _ClassVar[int]
    BULK_REMOVE_FIELD_NUMBER: _ClassVar[int]
    community: bytes
    actor: bytes
    at: int
    nonce: bytes
    update_settings: CommunitySettings
    create_join_key: CreateJoinKey
    revoke_join_key: RevokeJoinKey
    create_channel: CreateChannel
    edit_channel: EditChannel
    delete_channel: DeleteChannel
    create_role: CreateRole
    edit_role: EditRole
    delete_role: DeleteRole
    reorder_roles: ReorderRoles
    set_channel_override: SetChannelOverride
    assign_roles: AssignRoles
    kick: Kick
    ban: BanMember
    unban: Unban
    approve: Approve
    deny: Deny
    raid_revoke: RaidRevoke
    replace_member: ReplaceMember
    prepare_key_rotation: PrepareKeyRotation
    commit_key_rotation: CommitKeyRotation
    server_backup: ServerBackup
    create_community: CreateCommunity
    invite_owner: InviteOwner
    submit_owner_key: SubmitOwnerKey
    change_owners: ChangeOwners
    create_category: CreateCategory
    edit_category: EditCategory
    delete_category: DeleteCategory
    reorder_categories: ReorderCategories
    reorder_channels: ReorderChannels
    voice_mute: VoiceMute
    voice_deafen: VoiceDeafen
    voice_move: VoiceMove
    set_nickname: SetNickname
    timeout: TimeoutMember
    bulk_delete: BulkDelete
    save_automod_rule: SaveAutoModRule
    delete_automod_rule: DeleteAutoModRule
    resolve_report: ResolveReport
    add_emoji: AddEmoji
    rename_emoji: RenameEmoji
    delete_emoji: DeleteEmoji
    bulk_remove: BulkRemove
    def __init__(self, community: _Optional[bytes] = ..., actor: _Optional[bytes] = ..., at: _Optional[int] = ..., nonce: _Optional[bytes] = ..., update_settings: _Optional[_Union[CommunitySettings, _Mapping]] = ..., create_join_key: _Optional[_Union[CreateJoinKey, _Mapping]] = ..., revoke_join_key: _Optional[_Union[RevokeJoinKey, _Mapping]] = ..., create_channel: _Optional[_Union[CreateChannel, _Mapping]] = ..., edit_channel: _Optional[_Union[EditChannel, _Mapping]] = ..., delete_channel: _Optional[_Union[DeleteChannel, _Mapping]] = ..., create_role: _Optional[_Union[CreateRole, _Mapping]] = ..., edit_role: _Optional[_Union[EditRole, _Mapping]] = ..., delete_role: _Optional[_Union[DeleteRole, _Mapping]] = ..., reorder_roles: _Optional[_Union[ReorderRoles, _Mapping]] = ..., set_channel_override: _Optional[_Union[SetChannelOverride, _Mapping]] = ..., assign_roles: _Optional[_Union[AssignRoles, _Mapping]] = ..., kick: _Optional[_Union[Kick, _Mapping]] = ..., ban: _Optional[_Union[BanMember, _Mapping]] = ..., unban: _Optional[_Union[Unban, _Mapping]] = ..., approve: _Optional[_Union[Approve, _Mapping]] = ..., deny: _Optional[_Union[Deny, _Mapping]] = ..., raid_revoke: _Optional[_Union[RaidRevoke, _Mapping]] = ..., replace_member: _Optional[_Union[ReplaceMember, _Mapping]] = ..., prepare_key_rotation: _Optional[_Union[PrepareKeyRotation, _Mapping]] = ..., commit_key_rotation: _Optional[_Union[CommitKeyRotation, _Mapping]] = ..., server_backup: _Optional[_Union[ServerBackup, _Mapping]] = ..., create_community: _Optional[_Union[CreateCommunity, _Mapping]] = ..., invite_owner: _Optional[_Union[InviteOwner, _Mapping]] = ..., submit_owner_key: _Optional[_Union[SubmitOwnerKey, _Mapping]] = ..., change_owners: _Optional[_Union[ChangeOwners, _Mapping]] = ..., create_category: _Optional[_Union[CreateCategory, _Mapping]] = ..., edit_category: _Optional[_Union[EditCategory, _Mapping]] = ..., delete_category: _Optional[_Union[DeleteCategory, _Mapping]] = ..., reorder_categories: _Optional[_Union[ReorderCategories, _Mapping]] = ..., reorder_channels: _Optional[_Union[ReorderChannels, _Mapping]] = ..., voice_mute: _Optional[_Union[VoiceMute, _Mapping]] = ..., voice_deafen: _Optional[_Union[VoiceDeafen, _Mapping]] = ..., voice_move: _Optional[_Union[VoiceMove, _Mapping]] = ..., set_nickname: _Optional[_Union[SetNickname, _Mapping]] = ..., timeout: _Optional[_Union[TimeoutMember, _Mapping]] = ..., bulk_delete: _Optional[_Union[BulkDelete, _Mapping]] = ..., save_automod_rule: _Optional[_Union[SaveAutoModRule, _Mapping]] = ..., delete_automod_rule: _Optional[_Union[DeleteAutoModRule, _Mapping]] = ..., resolve_report: _Optional[_Union[ResolveReport, _Mapping]] = ..., add_emoji: _Optional[_Union[AddEmoji, _Mapping]] = ..., rename_emoji: _Optional[_Union[RenameEmoji, _Mapping]] = ..., delete_emoji: _Optional[_Union[DeleteEmoji, _Mapping]] = ..., bulk_remove: _Optional[_Union[BulkRemove, _Mapping]] = ...) -> None: ...

class CreateJoinKey(_message.Message):
    __slots__ = ("label", "expires_in_seconds", "max_uses", "role_ids", "request_only", "public")
    LABEL_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_IN_SECONDS_FIELD_NUMBER: _ClassVar[int]
    MAX_USES_FIELD_NUMBER: _ClassVar[int]
    ROLE_IDS_FIELD_NUMBER: _ClassVar[int]
    REQUEST_ONLY_FIELD_NUMBER: _ClassVar[int]
    PUBLIC_FIELD_NUMBER: _ClassVar[int]
    label: str
    expires_in_seconds: int
    max_uses: int
    role_ids: _containers.RepeatedScalarFieldContainer[int]
    request_only: bool
    public: bool
    def __init__(self, label: _Optional[str] = ..., expires_in_seconds: _Optional[int] = ..., max_uses: _Optional[int] = ..., role_ids: _Optional[_Iterable[int]] = ..., request_only: _Optional[bool] = ..., public: _Optional[bool] = ...) -> None: ...

class RevokeJoinKey(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: int
    def __init__(self, id: _Optional[int] = ...) -> None: ...

class CreateChannel(_message.Message):
    __slots__ = ("name", "kind", "category_id", "description")
    NAME_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    CATEGORY_ID_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    name: str
    kind: ChannelKind
    category_id: int
    description: str
    def __init__(self, name: _Optional[str] = ..., kind: _Optional[_Union[ChannelKind, str]] = ..., category_id: _Optional[int] = ..., description: _Optional[str] = ...) -> None: ...

class EditChannel(_message.Message):
    __slots__ = ("id", "name", "position", "message_retention_days", "file_retention_days", "clear_retention", "description", "category_id", "sync_permissions", "slowmode_seconds", "attachments", "link_previews", "reactions", "history_for_new_members")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    POSITION_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_RETENTION_DAYS_FIELD_NUMBER: _ClassVar[int]
    FILE_RETENTION_DAYS_FIELD_NUMBER: _ClassVar[int]
    CLEAR_RETENTION_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    CATEGORY_ID_FIELD_NUMBER: _ClassVar[int]
    SYNC_PERMISSIONS_FIELD_NUMBER: _ClassVar[int]
    SLOWMODE_SECONDS_FIELD_NUMBER: _ClassVar[int]
    ATTACHMENTS_FIELD_NUMBER: _ClassVar[int]
    LINK_PREVIEWS_FIELD_NUMBER: _ClassVar[int]
    REACTIONS_FIELD_NUMBER: _ClassVar[int]
    HISTORY_FOR_NEW_MEMBERS_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    position: int
    message_retention_days: int
    file_retention_days: int
    clear_retention: bool
    description: str
    category_id: int
    sync_permissions: bool
    slowmode_seconds: int
    attachments: ChannelToggle
    link_previews: ChannelToggle
    reactions: ChannelToggle
    history_for_new_members: ChannelToggle
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ..., position: _Optional[int] = ..., message_retention_days: _Optional[int] = ..., file_retention_days: _Optional[int] = ..., clear_retention: _Optional[bool] = ..., description: _Optional[str] = ..., category_id: _Optional[int] = ..., sync_permissions: _Optional[bool] = ..., slowmode_seconds: _Optional[int] = ..., attachments: _Optional[_Union[ChannelToggle, str]] = ..., link_previews: _Optional[_Union[ChannelToggle, str]] = ..., reactions: _Optional[_Union[ChannelToggle, str]] = ..., history_for_new_members: _Optional[_Union[ChannelToggle, str]] = ...) -> None: ...

class CreateCategory(_message.Message):
    __slots__ = ("name",)
    NAME_FIELD_NUMBER: _ClassVar[int]
    name: str
    def __init__(self, name: _Optional[str] = ...) -> None: ...

class EditCategory(_message.Message):
    __slots__ = ("id", "name")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ...) -> None: ...

class DeleteCategory(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: int
    def __init__(self, id: _Optional[int] = ...) -> None: ...

class ReorderCategories(_message.Message):
    __slots__ = ("category_ids",)
    CATEGORY_IDS_FIELD_NUMBER: _ClassVar[int]
    category_ids: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, category_ids: _Optional[_Iterable[int]] = ...) -> None: ...

class ReorderChannels(_message.Message):
    __slots__ = ("category_id", "channel_ids")
    CATEGORY_ID_FIELD_NUMBER: _ClassVar[int]
    CHANNEL_IDS_FIELD_NUMBER: _ClassVar[int]
    category_id: int
    channel_ids: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, category_id: _Optional[int] = ..., channel_ids: _Optional[_Iterable[int]] = ...) -> None: ...

class DeleteChannel(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: int
    def __init__(self, id: _Optional[int] = ...) -> None: ...

class CreateRole(_message.Message):
    __slots__ = ("name", "permissions", "color", "hoist", "mentionable")
    NAME_FIELD_NUMBER: _ClassVar[int]
    PERMISSIONS_FIELD_NUMBER: _ClassVar[int]
    COLOR_FIELD_NUMBER: _ClassVar[int]
    HOIST_FIELD_NUMBER: _ClassVar[int]
    MENTIONABLE_FIELD_NUMBER: _ClassVar[int]
    name: str
    permissions: int
    color: int
    hoist: bool
    mentionable: bool
    def __init__(self, name: _Optional[str] = ..., permissions: _Optional[int] = ..., color: _Optional[int] = ..., hoist: _Optional[bool] = ..., mentionable: _Optional[bool] = ...) -> None: ...

class EditRole(_message.Message):
    __slots__ = ("id", "name", "permissions", "color", "hoist", "mentionable")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    PERMISSIONS_FIELD_NUMBER: _ClassVar[int]
    COLOR_FIELD_NUMBER: _ClassVar[int]
    HOIST_FIELD_NUMBER: _ClassVar[int]
    MENTIONABLE_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    permissions: int
    color: int
    hoist: bool
    mentionable: bool
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ..., permissions: _Optional[int] = ..., color: _Optional[int] = ..., hoist: _Optional[bool] = ..., mentionable: _Optional[bool] = ...) -> None: ...

class DeleteRole(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: int
    def __init__(self, id: _Optional[int] = ...) -> None: ...

class ReorderRoles(_message.Message):
    __slots__ = ("role_ids",)
    ROLE_IDS_FIELD_NUMBER: _ClassVar[int]
    role_ids: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, role_ids: _Optional[_Iterable[int]] = ...) -> None: ...

class SetChannelOverride(_message.Message):
    __slots__ = ("channel_id", "role_id", "allow", "deny", "member", "category_id")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    ROLE_ID_FIELD_NUMBER: _ClassVar[int]
    ALLOW_FIELD_NUMBER: _ClassVar[int]
    DENY_FIELD_NUMBER: _ClassVar[int]
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    CATEGORY_ID_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    role_id: int
    allow: int
    deny: int
    member: bytes
    category_id: int
    def __init__(self, channel_id: _Optional[int] = ..., role_id: _Optional[int] = ..., allow: _Optional[int] = ..., deny: _Optional[int] = ..., member: _Optional[bytes] = ..., category_id: _Optional[int] = ...) -> None: ...

class AssignRoles(_message.Message):
    __slots__ = ("member", "role_ids")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    ROLE_IDS_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    role_ids: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, member: _Optional[bytes] = ..., role_ids: _Optional[_Iterable[int]] = ...) -> None: ...

class Kick(_message.Message):
    __slots__ = ("member", "reason")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    reason: str
    def __init__(self, member: _Optional[bytes] = ..., reason: _Optional[str] = ...) -> None: ...

class BanMember(_message.Message):
    __slots__ = ("member", "reason", "delete_message_seconds", "block_network", "block_seconds")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    DELETE_MESSAGE_SECONDS_FIELD_NUMBER: _ClassVar[int]
    BLOCK_NETWORK_FIELD_NUMBER: _ClassVar[int]
    BLOCK_SECONDS_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    reason: str
    delete_message_seconds: int
    block_network: bool
    block_seconds: int
    def __init__(self, member: _Optional[bytes] = ..., reason: _Optional[str] = ..., delete_message_seconds: _Optional[int] = ..., block_network: _Optional[bool] = ..., block_seconds: _Optional[int] = ...) -> None: ...

class Unban(_message.Message):
    __slots__ = ("member",)
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    def __init__(self, member: _Optional[bytes] = ...) -> None: ...

class Approve(_message.Message):
    __slots__ = ("member",)
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    def __init__(self, member: _Optional[bytes] = ...) -> None: ...

class Deny(_message.Message):
    __slots__ = ("member",)
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    def __init__(self, member: _Optional[bytes] = ...) -> None: ...

class RaidRevoke(_message.Message):
    __slots__ = ("join_key_id", "hours")
    JOIN_KEY_ID_FIELD_NUMBER: _ClassVar[int]
    HOURS_FIELD_NUMBER: _ClassVar[int]
    join_key_id: int
    hours: int
    def __init__(self, join_key_id: _Optional[int] = ..., hours: _Optional[int] = ...) -> None: ...

class ReplaceMember(_message.Message):
    __slots__ = ("old_key", "new_key")
    OLD_KEY_FIELD_NUMBER: _ClassVar[int]
    NEW_KEY_FIELD_NUMBER: _ClassVar[int]
    old_key: bytes
    new_key: bytes
    def __init__(self, old_key: _Optional[bytes] = ..., new_key: _Optional[bytes] = ...) -> None: ...

class InviteOwner(_message.Message):
    __slots__ = ("member", "cancel")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    CANCEL_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    cancel: bool
    def __init__(self, member: _Optional[bytes] = ..., cancel: _Optional[bool] = ...) -> None: ...

class SubmitOwnerKey(_message.Message):
    __slots__ = ("add",)
    ADD_FIELD_NUMBER: _ClassVar[int]
    add: _session_pb2.OwnerAdd
    def __init__(self, add: _Optional[_Union[_session_pb2.OwnerAdd, _Mapping]] = ...) -> None: ...

class ChangeOwners(_message.Message):
    __slots__ = ("change", "cert")
    CHANGE_FIELD_NUMBER: _ClassVar[int]
    CERT_FIELD_NUMBER: _ClassVar[int]
    change: _session_pb2.SignedOwnerChange
    cert: _session_pb2.SignedServerCert
    def __init__(self, change: _Optional[_Union[_session_pb2.SignedOwnerChange, _Mapping]] = ..., cert: _Optional[_Union[_session_pb2.SignedServerCert, _Mapping]] = ...) -> None: ...

class SetNickname(_message.Message):
    __slots__ = ("member", "nickname")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    NICKNAME_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    nickname: str
    def __init__(self, member: _Optional[bytes] = ..., nickname: _Optional[str] = ...) -> None: ...

class PrepareKeyRotation(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class CommitKeyRotation(_message.Message):
    __slots__ = ("cert",)
    CERT_FIELD_NUMBER: _ClassVar[int]
    cert: _session_pb2.SignedServerCert
    def __init__(self, cert: _Optional[_Union[_session_pb2.SignedServerCert, _Mapping]] = ...) -> None: ...

class ServerBackup(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class CreateCommunity(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class AdminResult(_message.Message):
    __slots__ = ("join_key", "id", "new_server_key", "backup_path", "removed", "deleted", "network_block", "claim_code")
    JOIN_KEY_FIELD_NUMBER: _ClassVar[int]
    ID_FIELD_NUMBER: _ClassVar[int]
    NEW_SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    BACKUP_PATH_FIELD_NUMBER: _ClassVar[int]
    REMOVED_FIELD_NUMBER: _ClassVar[int]
    DELETED_FIELD_NUMBER: _ClassVar[int]
    NETWORK_BLOCK_FIELD_NUMBER: _ClassVar[int]
    CLAIM_CODE_FIELD_NUMBER: _ClassVar[int]
    join_key: JoinKey
    id: int
    new_server_key: bytes
    backup_path: str
    removed: int
    deleted: int
    network_block: NetworkBlockStatus
    claim_code: str
    def __init__(self, join_key: _Optional[_Union[JoinKey, _Mapping]] = ..., id: _Optional[int] = ..., new_server_key: _Optional[bytes] = ..., backup_path: _Optional[str] = ..., removed: _Optional[int] = ..., deleted: _Optional[int] = ..., network_block: _Optional[_Union[NetworkBlockStatus, str]] = ..., claim_code: _Optional[str] = ...) -> None: ...

class AdoptServer(_message.Message):
    __slots__ = ("secret", "cert")
    SECRET_FIELD_NUMBER: _ClassVar[int]
    CERT_FIELD_NUMBER: _ClassVar[int]
    secret: bytes
    cert: _session_pb2.SignedServerCert
    def __init__(self, secret: _Optional[bytes] = ..., cert: _Optional[_Union[_session_pb2.SignedServerCert, _Mapping]] = ...) -> None: ...

class KeyPackage(_message.Message):
    __slots__ = ("device", "package")
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    PACKAGE_FIELD_NUMBER: _ClassVar[int]
    device: bytes
    package: bytes
    def __init__(self, device: _Optional[bytes] = ..., package: _Optional[bytes] = ...) -> None: ...

class DeviceKeyCount(_message.Message):
    __slots__ = ("device", "left")
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    LEFT_FIELD_NUMBER: _ClassVar[int]
    device: bytes
    left: int
    def __init__(self, device: _Optional[bytes] = ..., left: _Optional[int] = ...) -> None: ...

class PublishContactCard(_message.Message):
    __slots__ = ("card", "key_packages", "devices")
    CARD_FIELD_NUMBER: _ClassVar[int]
    KEY_PACKAGES_FIELD_NUMBER: _ClassVar[int]
    DEVICES_FIELD_NUMBER: _ClassVar[int]
    card: bytes
    key_packages: _containers.RepeatedCompositeFieldContainer[KeyPackage]
    devices: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, card: _Optional[bytes] = ..., key_packages: _Optional[_Iterable[_Union[KeyPackage, _Mapping]]] = ..., devices: _Optional[_Iterable[bytes]] = ...) -> None: ...

class AddKeyPackages(_message.Message):
    __slots__ = ("key_packages",)
    KEY_PACKAGES_FIELD_NUMBER: _ClassVar[int]
    key_packages: _containers.RepeatedCompositeFieldContainer[KeyPackage]
    def __init__(self, key_packages: _Optional[_Iterable[_Union[KeyPackage, _Mapping]]] = ...) -> None: ...

class GetContactCard(_message.Message):
    __slots__ = ("member",)
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    def __init__(self, member: _Optional[bytes] = ...) -> None: ...

class ContactCardResult(_message.Message):
    __slots__ = ("card", "key_packages", "left")
    CARD_FIELD_NUMBER: _ClassVar[int]
    KEY_PACKAGES_FIELD_NUMBER: _ClassVar[int]
    LEFT_FIELD_NUMBER: _ClassVar[int]
    card: bytes
    key_packages: _containers.RepeatedCompositeFieldContainer[KeyPackage]
    left: _containers.RepeatedCompositeFieldContainer[DeviceKeyCount]
    def __init__(self, card: _Optional[bytes] = ..., key_packages: _Optional[_Iterable[_Union[KeyPackage, _Mapping]]] = ..., left: _Optional[_Iterable[_Union[DeviceKeyCount, _Mapping]]] = ...) -> None: ...

class VoiceJoin(_message.Message):
    __slots__ = ("channel_id", "rejoin", "failed_relay")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    REJOIN_FIELD_NUMBER: _ClassVar[int]
    FAILED_RELAY_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    rejoin: bool
    failed_relay: str
    def __init__(self, channel_id: _Optional[int] = ..., rejoin: _Optional[bool] = ..., failed_relay: _Optional[str] = ...) -> None: ...

class VoiceLeave(_message.Message):
    __slots__ = ("channel_id",)
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    def __init__(self, channel_id: _Optional[int] = ...) -> None: ...

class VoiceState(_message.Message):
    __slots__ = ("channel_id", "self_mute", "self_deaf", "camera", "screen", "priority")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    SELF_MUTE_FIELD_NUMBER: _ClassVar[int]
    SELF_DEAF_FIELD_NUMBER: _ClassVar[int]
    CAMERA_FIELD_NUMBER: _ClassVar[int]
    SCREEN_FIELD_NUMBER: _ClassVar[int]
    PRIORITY_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    self_mute: bool
    self_deaf: bool
    camera: bool
    screen: bool
    priority: bool
    def __init__(self, channel_id: _Optional[int] = ..., self_mute: _Optional[bool] = ..., self_deaf: _Optional[bool] = ..., camera: _Optional[bool] = ..., screen: _Optional[bool] = ..., priority: _Optional[bool] = ...) -> None: ...

class VoiceParticipant(_message.Message):
    __slots__ = ("member", "display_name", "listen_only", "participant", "slot", "self_mute", "self_deaf", "server_mute", "server_deaf", "camera", "screen", "priority")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    LISTEN_ONLY_FIELD_NUMBER: _ClassVar[int]
    PARTICIPANT_FIELD_NUMBER: _ClassVar[int]
    SLOT_FIELD_NUMBER: _ClassVar[int]
    SELF_MUTE_FIELD_NUMBER: _ClassVar[int]
    SELF_DEAF_FIELD_NUMBER: _ClassVar[int]
    SERVER_MUTE_FIELD_NUMBER: _ClassVar[int]
    SERVER_DEAF_FIELD_NUMBER: _ClassVar[int]
    CAMERA_FIELD_NUMBER: _ClassVar[int]
    SCREEN_FIELD_NUMBER: _ClassVar[int]
    PRIORITY_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    display_name: str
    listen_only: bool
    participant: bytes
    slot: int
    self_mute: bool
    self_deaf: bool
    server_mute: bool
    server_deaf: bool
    camera: bool
    screen: bool
    priority: bool
    def __init__(self, member: _Optional[bytes] = ..., display_name: _Optional[str] = ..., listen_only: _Optional[bool] = ..., participant: _Optional[bytes] = ..., slot: _Optional[int] = ..., self_mute: _Optional[bool] = ..., self_deaf: _Optional[bool] = ..., server_mute: _Optional[bool] = ..., server_deaf: _Optional[bool] = ..., camera: _Optional[bool] = ..., screen: _Optional[bool] = ..., priority: _Optional[bool] = ...) -> None: ...

class VoiceSession(_message.Message):
    __slots__ = ("channel_id", "call_key", "key_epoch", "participants", "relay", "listen_only", "grant", "participant", "slot", "can_video", "can_screen", "can_voice_activity", "priority", "server_mute", "server_deaf", "moved_to", "ended", "control")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    CALL_KEY_FIELD_NUMBER: _ClassVar[int]
    KEY_EPOCH_FIELD_NUMBER: _ClassVar[int]
    PARTICIPANTS_FIELD_NUMBER: _ClassVar[int]
    RELAY_FIELD_NUMBER: _ClassVar[int]
    LISTEN_ONLY_FIELD_NUMBER: _ClassVar[int]
    GRANT_FIELD_NUMBER: _ClassVar[int]
    PARTICIPANT_FIELD_NUMBER: _ClassVar[int]
    SLOT_FIELD_NUMBER: _ClassVar[int]
    CAN_VIDEO_FIELD_NUMBER: _ClassVar[int]
    CAN_SCREEN_FIELD_NUMBER: _ClassVar[int]
    CAN_VOICE_ACTIVITY_FIELD_NUMBER: _ClassVar[int]
    PRIORITY_FIELD_NUMBER: _ClassVar[int]
    SERVER_MUTE_FIELD_NUMBER: _ClassVar[int]
    SERVER_DEAF_FIELD_NUMBER: _ClassVar[int]
    MOVED_TO_FIELD_NUMBER: _ClassVar[int]
    ENDED_FIELD_NUMBER: _ClassVar[int]
    CONTROL_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    call_key: bytes
    key_epoch: int
    participants: _containers.RepeatedCompositeFieldContainer[VoiceParticipant]
    relay: str
    listen_only: bool
    grant: _call_pb2.SignedCallGrant
    participant: bytes
    slot: int
    can_video: bool
    can_screen: bool
    can_voice_activity: bool
    priority: bool
    server_mute: bool
    server_deaf: bool
    moved_to: int
    ended: str
    control: _call_pb2.SignedCallControl
    def __init__(self, channel_id: _Optional[int] = ..., call_key: _Optional[bytes] = ..., key_epoch: _Optional[int] = ..., participants: _Optional[_Iterable[_Union[VoiceParticipant, _Mapping]]] = ..., relay: _Optional[str] = ..., listen_only: _Optional[bool] = ..., grant: _Optional[_Union[_call_pb2.SignedCallGrant, _Mapping]] = ..., participant: _Optional[bytes] = ..., slot: _Optional[int] = ..., can_video: _Optional[bool] = ..., can_screen: _Optional[bool] = ..., can_voice_activity: _Optional[bool] = ..., priority: _Optional[bool] = ..., server_mute: _Optional[bool] = ..., server_deaf: _Optional[bool] = ..., moved_to: _Optional[int] = ..., ended: _Optional[str] = ..., control: _Optional[_Union[_call_pb2.SignedCallControl, _Mapping]] = ...) -> None: ...

class VoiceUpdate(_message.Message):
    __slots__ = ("session",)
    SESSION_FIELD_NUMBER: _ClassVar[int]
    session: VoiceSession
    def __init__(self, session: _Optional[_Union[VoiceSession, _Mapping]] = ...) -> None: ...

class VoiceRooms(_message.Message):
    __slots__ = ("rooms",)
    ROOMS_FIELD_NUMBER: _ClassVar[int]
    rooms: _containers.RepeatedCompositeFieldContainer[VoiceRoom]
    def __init__(self, rooms: _Optional[_Iterable[_Union[VoiceRoom, _Mapping]]] = ...) -> None: ...

class VoiceRoom(_message.Message):
    __slots__ = ("channel_id", "participants")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    PARTICIPANTS_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    participants: _containers.RepeatedCompositeFieldContainer[VoiceParticipant]
    def __init__(self, channel_id: _Optional[int] = ..., participants: _Optional[_Iterable[_Union[VoiceParticipant, _Mapping]]] = ...) -> None: ...

class VoiceMute(_message.Message):
    __slots__ = ("member", "muted")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    MUTED_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    muted: bool
    def __init__(self, member: _Optional[bytes] = ..., muted: _Optional[bool] = ...) -> None: ...

class VoiceDeafen(_message.Message):
    __slots__ = ("member", "deafened")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    DEAFENED_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    deafened: bool
    def __init__(self, member: _Optional[bytes] = ..., deafened: _Optional[bool] = ...) -> None: ...

class VoiceMove(_message.Message):
    __slots__ = ("member", "channel_id")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    channel_id: int
    def __init__(self, member: _Optional[bytes] = ..., channel_id: _Optional[int] = ...) -> None: ...

class SignedEnvelope(_message.Message):
    __slots__ = ("envelope", "signature")
    ENVELOPE_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    envelope: bytes
    signature: bytes
    def __init__(self, envelope: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class ChannelEnvelope(_message.Message):
    __slots__ = ("channel_id", "key_epoch", "nonce", "ciphertext")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    KEY_EPOCH_FIELD_NUMBER: _ClassVar[int]
    NONCE_FIELD_NUMBER: _ClassVar[int]
    CIPHERTEXT_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    key_epoch: int
    nonce: bytes
    ciphertext: bytes
    def __init__(self, channel_id: _Optional[int] = ..., key_epoch: _Optional[int] = ..., nonce: _Optional[bytes] = ..., ciphertext: _Optional[bytes] = ...) -> None: ...

class ChannelEvent(_message.Message):
    __slots__ = ("message", "edited", "deleted", "typing", "update", "deleted_many")
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    EDITED_FIELD_NUMBER: _ClassVar[int]
    DELETED_FIELD_NUMBER: _ClassVar[int]
    TYPING_FIELD_NUMBER: _ClassVar[int]
    UPDATE_FIELD_NUMBER: _ClassVar[int]
    DELETED_MANY_FIELD_NUMBER: _ClassVar[int]
    message: Message
    edited: Message
    deleted: int
    typing: TypingEvent
    update: MessageUpdate
    deleted_many: DeletedMessages
    def __init__(self, message: _Optional[_Union[Message, _Mapping]] = ..., edited: _Optional[_Union[Message, _Mapping]] = ..., deleted: _Optional[int] = ..., typing: _Optional[_Union[TypingEvent, _Mapping]] = ..., update: _Optional[_Union[MessageUpdate, _Mapping]] = ..., deleted_many: _Optional[_Union[DeletedMessages, _Mapping]] = ...) -> None: ...

class MessageUpdate(_message.Message):
    __slots__ = ("message_id", "channel_id", "reactions", "pinned", "preview")
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    REACTIONS_FIELD_NUMBER: _ClassVar[int]
    PINNED_FIELD_NUMBER: _ClassVar[int]
    PREVIEW_FIELD_NUMBER: _ClassVar[int]
    message_id: int
    channel_id: int
    reactions: ReactionList
    pinned: bool
    preview: LinkPreview
    def __init__(self, message_id: _Optional[int] = ..., channel_id: _Optional[int] = ..., reactions: _Optional[_Union[ReactionList, _Mapping]] = ..., pinned: _Optional[bool] = ..., preview: _Optional[_Union[LinkPreview, _Mapping]] = ...) -> None: ...

class ReactionList(_message.Message):
    __slots__ = ("reactions",)
    REACTIONS_FIELD_NUMBER: _ClassVar[int]
    reactions: _containers.RepeatedCompositeFieldContainer[Reaction]
    def __init__(self, reactions: _Optional[_Iterable[_Union[Reaction, _Mapping]]] = ...) -> None: ...

class TypingEvent(_message.Message):
    __slots__ = ("member", "name")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    name: str
    def __init__(self, member: _Optional[bytes] = ..., name: _Optional[str] = ...) -> None: ...

class React(_message.Message):
    __slots__ = ("message_id", "emoji", "remove")
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    EMOJI_FIELD_NUMBER: _ClassVar[int]
    REMOVE_FIELD_NUMBER: _ClassVar[int]
    message_id: int
    emoji: str
    remove: bool
    def __init__(self, message_id: _Optional[int] = ..., emoji: _Optional[str] = ..., remove: _Optional[bool] = ...) -> None: ...

class Pin(_message.Message):
    __slots__ = ("message_id", "unpin")
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    UNPIN_FIELD_NUMBER: _ClassVar[int]
    message_id: int
    unpin: bool
    def __init__(self, message_id: _Optional[int] = ..., unpin: _Optional[bool] = ...) -> None: ...

class ListPins(_message.Message):
    __slots__ = ("channel_id",)
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    def __init__(self, channel_id: _Optional[int] = ...) -> None: ...

class PinList(_message.Message):
    __slots__ = ("messages",)
    MESSAGES_FIELD_NUMBER: _ClassVar[int]
    messages: _containers.RepeatedCompositeFieldContainer[Message]
    def __init__(self, messages: _Optional[_Iterable[_Union[Message, _Mapping]]] = ...) -> None: ...

class Typing(_message.Message):
    __slots__ = ("channel_id",)
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    def __init__(self, channel_id: _Optional[int] = ...) -> None: ...

class UpdateProfile(_message.Message):
    __slots__ = ("about", "pronouns")
    ABOUT_FIELD_NUMBER: _ClassVar[int]
    PRONOUNS_FIELD_NUMBER: _ClassVar[int]
    about: str
    pronouns: str
    def __init__(self, about: _Optional[str] = ..., pronouns: _Optional[str] = ...) -> None: ...

class PresenceUpdate(_message.Message):
    __slots__ = ("member", "online")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    ONLINE_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    online: bool
    def __init__(self, member: _Optional[bytes] = ..., online: _Optional[bool] = ...) -> None: ...

class PresenceBatch(_message.Message):
    __slots__ = ("updates", "statuses")
    UPDATES_FIELD_NUMBER: _ClassVar[int]
    STATUSES_FIELD_NUMBER: _ClassVar[int]
    updates: _containers.RepeatedCompositeFieldContainer[PresenceUpdate]
    statuses: _containers.RepeatedCompositeFieldContainer[StatusEntry]
    def __init__(self, updates: _Optional[_Iterable[_Union[PresenceUpdate, _Mapping]]] = ..., statuses: _Optional[_Iterable[_Union[StatusEntry, _Mapping]]] = ...) -> None: ...

class SetStatusDevice(_message.Message):
    __slots__ = ("entry", "device")
    ENTRY_FIELD_NUMBER: _ClassVar[int]
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    entry: bytes
    device: bytes
    def __init__(self, entry: _Optional[bytes] = ..., device: _Optional[bytes] = ...) -> None: ...

class SetStatus(_message.Message):
    __slots__ = ("blob", "invisible", "keep_blob")
    BLOB_FIELD_NUMBER: _ClassVar[int]
    INVISIBLE_FIELD_NUMBER: _ClassVar[int]
    KEEP_BLOB_FIELD_NUMBER: _ClassVar[int]
    blob: bytes
    invisible: bool
    keep_blob: bool
    def __init__(self, blob: _Optional[bytes] = ..., invisible: _Optional[bool] = ..., keep_blob: _Optional[bool] = ...) -> None: ...

class ListStatuses(_message.Message):
    __slots__ = ("after",)
    AFTER_FIELD_NUMBER: _ClassVar[int]
    after: bytes
    def __init__(self, after: _Optional[bytes] = ...) -> None: ...

class StatusEntry(_message.Message):
    __slots__ = ("member", "devices", "blob")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    DEVICES_FIELD_NUMBER: _ClassVar[int]
    BLOB_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    devices: _containers.RepeatedScalarFieldContainer[bytes]
    blob: bytes
    def __init__(self, member: _Optional[bytes] = ..., devices: _Optional[_Iterable[bytes]] = ..., blob: _Optional[bytes] = ...) -> None: ...

class StatusList(_message.Message):
    __slots__ = ("entries", "invisible", "more")
    ENTRIES_FIELD_NUMBER: _ClassVar[int]
    INVISIBLE_FIELD_NUMBER: _ClassVar[int]
    MORE_FIELD_NUMBER: _ClassVar[int]
    entries: _containers.RepeatedCompositeFieldContainer[StatusEntry]
    invisible: bool
    more: bool
    def __init__(self, entries: _Optional[_Iterable[_Union[StatusEntry, _Mapping]]] = ..., invisible: _Optional[bool] = ..., more: _Optional[bool] = ...) -> None: ...

class StatusKeyDrop(_message.Message):
    __slots__ = ("member", "device", "envelope")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    ENVELOPE_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    device: bytes
    envelope: bytes
    def __init__(self, member: _Optional[bytes] = ..., device: _Optional[bytes] = ..., envelope: _Optional[bytes] = ...) -> None: ...

class SendStatusKeys(_message.Message):
    __slots__ = ("drops",)
    DROPS_FIELD_NUMBER: _ClassVar[int]
    drops: _containers.RepeatedCompositeFieldContainer[StatusKeyDrop]
    def __init__(self, drops: _Optional[_Iterable[_Union[StatusKeyDrop, _Mapping]]] = ...) -> None: ...

class FetchStatusKeys(_message.Message):
    __slots__ = ("ack", "device")
    ACK_FIELD_NUMBER: _ClassVar[int]
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    ack: _containers.RepeatedScalarFieldContainer[int]
    device: bytes
    def __init__(self, ack: _Optional[_Iterable[int]] = ..., device: _Optional[bytes] = ...) -> None: ...

class StatusKeyItem(_message.Message):
    __slots__ = ("id", "sender", "device", "envelope")
    ID_FIELD_NUMBER: _ClassVar[int]
    SENDER_FIELD_NUMBER: _ClassVar[int]
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    ENVELOPE_FIELD_NUMBER: _ClassVar[int]
    id: int
    sender: bytes
    device: bytes
    envelope: bytes
    def __init__(self, id: _Optional[int] = ..., sender: _Optional[bytes] = ..., device: _Optional[bytes] = ..., envelope: _Optional[bytes] = ...) -> None: ...

class StatusKeys(_message.Message):
    __slots__ = ("items", "more")
    ITEMS_FIELD_NUMBER: _ClassVar[int]
    MORE_FIELD_NUMBER: _ClassVar[int]
    items: _containers.RepeatedCompositeFieldContainer[StatusKeyItem]
    more: bool
    def __init__(self, items: _Optional[_Iterable[_Union[StatusKeyItem, _Mapping]]] = ..., more: _Optional[bool] = ...) -> None: ...

class RequestStatusKey(_message.Message):
    __slots__ = ("member", "device")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    device: bytes
    def __init__(self, member: _Optional[bytes] = ..., device: _Optional[bytes] = ...) -> None: ...

class StatusKeyRequest(_message.Message):
    __slots__ = ("member", "device")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    device: bytes
    def __init__(self, member: _Optional[bytes] = ..., device: _Optional[bytes] = ...) -> None: ...

class DeletedMessages(_message.Message):
    __slots__ = ("ids",)
    IDS_FIELD_NUMBER: _ClassVar[int]
    ids: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, ids: _Optional[_Iterable[int]] = ...) -> None: ...

class TimeoutMember(_message.Message):
    __slots__ = ("member", "seconds", "reason")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    SECONDS_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    seconds: int
    reason: str
    def __init__(self, member: _Optional[bytes] = ..., seconds: _Optional[int] = ..., reason: _Optional[str] = ...) -> None: ...

class BulkRemove(_message.Message):
    __slots__ = ("members", "ban", "reason", "delete_message_seconds")
    MEMBERS_FIELD_NUMBER: _ClassVar[int]
    BAN_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    DELETE_MESSAGE_SECONDS_FIELD_NUMBER: _ClassVar[int]
    members: _containers.RepeatedScalarFieldContainer[bytes]
    ban: bool
    reason: str
    delete_message_seconds: int
    def __init__(self, members: _Optional[_Iterable[bytes]] = ..., ban: _Optional[bool] = ..., reason: _Optional[str] = ..., delete_message_seconds: _Optional[int] = ...) -> None: ...

class BulkDelete(_message.Message):
    __slots__ = ("channel_id", "message_ids", "last")
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_IDS_FIELD_NUMBER: _ClassVar[int]
    LAST_FIELD_NUMBER: _ClassVar[int]
    channel_id: int
    message_ids: _containers.RepeatedScalarFieldContainer[int]
    last: int
    def __init__(self, channel_id: _Optional[int] = ..., message_ids: _Optional[_Iterable[int]] = ..., last: _Optional[int] = ...) -> None: ...

class ReportMessage(_message.Message):
    __slots__ = ("message_id", "reason")
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    message_id: int
    reason: str
    def __init__(self, message_id: _Optional[int] = ..., reason: _Optional[str] = ...) -> None: ...

class ListReports(_message.Message):
    __slots__ = ("include_closed", "before_id", "limit")
    INCLUDE_CLOSED_FIELD_NUMBER: _ClassVar[int]
    BEFORE_ID_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    include_closed: bool
    before_id: int
    limit: int
    def __init__(self, include_closed: _Optional[bool] = ..., before_id: _Optional[int] = ..., limit: _Optional[int] = ...) -> None: ...

class Report(_message.Message):
    __slots__ = ("id", "kind", "status", "created_at", "reporter", "reporter_name", "message_id", "channel_id", "author", "author_name", "reason", "rule_id", "rule_name", "automod_action", "resolved_by", "resolved_by_name", "resolved_at")
    ID_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    REPORTER_FIELD_NUMBER: _ClassVar[int]
    REPORTER_NAME_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    CHANNEL_ID_FIELD_NUMBER: _ClassVar[int]
    AUTHOR_FIELD_NUMBER: _ClassVar[int]
    AUTHOR_NAME_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    RULE_ID_FIELD_NUMBER: _ClassVar[int]
    RULE_NAME_FIELD_NUMBER: _ClassVar[int]
    AUTOMOD_ACTION_FIELD_NUMBER: _ClassVar[int]
    RESOLVED_BY_FIELD_NUMBER: _ClassVar[int]
    RESOLVED_BY_NAME_FIELD_NUMBER: _ClassVar[int]
    RESOLVED_AT_FIELD_NUMBER: _ClassVar[int]
    id: int
    kind: ReportKind
    status: ReportStatus
    created_at: int
    reporter: bytes
    reporter_name: str
    message_id: int
    channel_id: int
    author: bytes
    author_name: str
    reason: str
    rule_id: int
    rule_name: str
    automod_action: str
    resolved_by: bytes
    resolved_by_name: str
    resolved_at: int
    def __init__(self, id: _Optional[int] = ..., kind: _Optional[_Union[ReportKind, str]] = ..., status: _Optional[_Union[ReportStatus, str]] = ..., created_at: _Optional[int] = ..., reporter: _Optional[bytes] = ..., reporter_name: _Optional[str] = ..., message_id: _Optional[int] = ..., channel_id: _Optional[int] = ..., author: _Optional[bytes] = ..., author_name: _Optional[str] = ..., reason: _Optional[str] = ..., rule_id: _Optional[int] = ..., rule_name: _Optional[str] = ..., automod_action: _Optional[str] = ..., resolved_by: _Optional[bytes] = ..., resolved_by_name: _Optional[str] = ..., resolved_at: _Optional[int] = ...) -> None: ...

class ReportList(_message.Message):
    __slots__ = ("reports", "more")
    REPORTS_FIELD_NUMBER: _ClassVar[int]
    MORE_FIELD_NUMBER: _ClassVar[int]
    reports: _containers.RepeatedCompositeFieldContainer[Report]
    more: bool
    def __init__(self, reports: _Optional[_Iterable[_Union[Report, _Mapping]]] = ..., more: _Optional[bool] = ...) -> None: ...

class ResolveReport(_message.Message):
    __slots__ = ("id", "dismiss")
    ID_FIELD_NUMBER: _ClassVar[int]
    DISMISS_FIELD_NUMBER: _ClassVar[int]
    id: int
    dismiss: bool
    def __init__(self, id: _Optional[int] = ..., dismiss: _Optional[bool] = ...) -> None: ...

class ModAlert(_message.Message):
    __slots__ = ("report",)
    REPORT_FIELD_NUMBER: _ClassVar[int]
    report: Report
    def __init__(self, report: _Optional[_Union[Report, _Mapping]] = ...) -> None: ...

class ModQueue(_message.Message):
    __slots__ = ("open_reports", "pending_members")
    OPEN_REPORTS_FIELD_NUMBER: _ClassVar[int]
    PENDING_MEMBERS_FIELD_NUMBER: _ClassVar[int]
    open_reports: int
    pending_members: int
    def __init__(self, open_reports: _Optional[int] = ..., pending_members: _Optional[int] = ...) -> None: ...

class AutoModRule(_message.Message):
    __slots__ = ("id", "name", "enabled", "trigger", "keywords", "patterns", "mention_limit", "allowed_domains", "block", "alert", "timeout_seconds", "exempt_role_ids", "exempt_channel_ids")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    ENABLED_FIELD_NUMBER: _ClassVar[int]
    TRIGGER_FIELD_NUMBER: _ClassVar[int]
    KEYWORDS_FIELD_NUMBER: _ClassVar[int]
    PATTERNS_FIELD_NUMBER: _ClassVar[int]
    MENTION_LIMIT_FIELD_NUMBER: _ClassVar[int]
    ALLOWED_DOMAINS_FIELD_NUMBER: _ClassVar[int]
    BLOCK_FIELD_NUMBER: _ClassVar[int]
    ALERT_FIELD_NUMBER: _ClassVar[int]
    TIMEOUT_SECONDS_FIELD_NUMBER: _ClassVar[int]
    EXEMPT_ROLE_IDS_FIELD_NUMBER: _ClassVar[int]
    EXEMPT_CHANNEL_IDS_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    enabled: bool
    trigger: AutoModTrigger
    keywords: _containers.RepeatedScalarFieldContainer[str]
    patterns: _containers.RepeatedScalarFieldContainer[str]
    mention_limit: int
    allowed_domains: _containers.RepeatedScalarFieldContainer[str]
    block: bool
    alert: bool
    timeout_seconds: int
    exempt_role_ids: _containers.RepeatedScalarFieldContainer[int]
    exempt_channel_ids: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ..., enabled: _Optional[bool] = ..., trigger: _Optional[_Union[AutoModTrigger, str]] = ..., keywords: _Optional[_Iterable[str]] = ..., patterns: _Optional[_Iterable[str]] = ..., mention_limit: _Optional[int] = ..., allowed_domains: _Optional[_Iterable[str]] = ..., block: _Optional[bool] = ..., alert: _Optional[bool] = ..., timeout_seconds: _Optional[int] = ..., exempt_role_ids: _Optional[_Iterable[int]] = ..., exempt_channel_ids: _Optional[_Iterable[int]] = ...) -> None: ...

class SaveAutoModRule(_message.Message):
    __slots__ = ("rule",)
    RULE_FIELD_NUMBER: _ClassVar[int]
    rule: AutoModRule
    def __init__(self, rule: _Optional[_Union[AutoModRule, _Mapping]] = ...) -> None: ...

class DeleteAutoModRule(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: int
    def __init__(self, id: _Optional[int] = ...) -> None: ...

class ListAutoModRules(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class AutoModRuleList(_message.Message):
    __slots__ = ("rules",)
    RULES_FIELD_NUMBER: _ClassVar[int]
    rules: _containers.RepeatedCompositeFieldContainer[AutoModRule]
    def __init__(self, rules: _Optional[_Iterable[_Union[AutoModRule, _Mapping]]] = ...) -> None: ...

class CustomEmoji(_message.Message):
    __slots__ = ("id", "name", "animated", "mime", "size", "version")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    ANIMATED_FIELD_NUMBER: _ClassVar[int]
    MIME_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    animated: bool
    mime: str
    size: int
    version: int
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ..., animated: _Optional[bool] = ..., mime: _Optional[str] = ..., size: _Optional[int] = ..., version: _Optional[int] = ...) -> None: ...

class AddEmoji(_message.Message):
    __slots__ = ("name", "sha256", "size")
    NAME_FIELD_NUMBER: _ClassVar[int]
    SHA256_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    name: str
    sha256: bytes
    size: int
    def __init__(self, name: _Optional[str] = ..., sha256: _Optional[bytes] = ..., size: _Optional[int] = ...) -> None: ...

class EmojiUpload(_message.Message):
    __slots__ = ("action", "image")
    ACTION_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    action: SignedAction
    image: bytes
    def __init__(self, action: _Optional[_Union[SignedAction, _Mapping]] = ..., image: _Optional[bytes] = ...) -> None: ...

class RenameEmoji(_message.Message):
    __slots__ = ("id", "name")
    ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    id: int
    name: str
    def __init__(self, id: _Optional[int] = ..., name: _Optional[str] = ...) -> None: ...

class DeleteEmoji(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: int
    def __init__(self, id: _Optional[int] = ...) -> None: ...

class GetEmoji(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: int
    def __init__(self, id: _Optional[int] = ...) -> None: ...

class EmojiImage(_message.Message):
    __slots__ = ("id", "image", "mime", "version")
    ID_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    MIME_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    id: int
    image: bytes
    mime: str
    version: int
    def __init__(self, id: _Optional[int] = ..., image: _Optional[bytes] = ..., mime: _Optional[str] = ..., version: _Optional[int] = ...) -> None: ...
