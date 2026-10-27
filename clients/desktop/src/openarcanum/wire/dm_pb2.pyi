from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class StatusState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    STATUS_STATE_UNSPECIFIED: _ClassVar[StatusState]
    STATUS_STATE_ONLINE: _ClassVar[StatusState]
    STATUS_STATE_IDLE: _ClassVar[StatusState]
    STATUS_STATE_DND: _ClassVar[StatusState]
    STATUS_STATE_OFFLINE: _ClassVar[StatusState]
    STATUS_STATE_CLEARED: _ClassVar[StatusState]

class DmCallKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    DM_CALL_KIND_UNSPECIFIED: _ClassVar[DmCallKind]
    DM_CALL_KIND_START: _ClassVar[DmCallKind]
    DM_CALL_KIND_JOIN: _ClassVar[DmCallKind]
    DM_CALL_KIND_STATE: _ClassVar[DmCallKind]
    DM_CALL_KIND_LEAVE: _ClassVar[DmCallKind]
STATUS_STATE_UNSPECIFIED: StatusState
STATUS_STATE_ONLINE: StatusState
STATUS_STATE_IDLE: StatusState
STATUS_STATE_DND: StatusState
STATUS_STATE_OFFLINE: StatusState
STATUS_STATE_CLEARED: StatusState
DM_CALL_KIND_UNSPECIFIED: DmCallKind
DM_CALL_KIND_START: DmCallKind
DM_CALL_KIND_JOIN: DmCallKind
DM_CALL_KIND_STATE: DmCallKind
DM_CALL_KIND_LEAVE: DmCallKind

class DmDevice(_message.Message):
    __slots__ = ("device_id", "curve25519", "ed25519", "name", "added_at", "device_signature")
    DEVICE_ID_FIELD_NUMBER: _ClassVar[int]
    CURVE25519_FIELD_NUMBER: _ClassVar[int]
    ED25519_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    ADDED_AT_FIELD_NUMBER: _ClassVar[int]
    DEVICE_SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    device_id: bytes
    curve25519: bytes
    ed25519: bytes
    name: str
    added_at: int
    device_signature: bytes
    def __init__(self, device_id: _Optional[bytes] = ..., curve25519: _Optional[bytes] = ..., ed25519: _Optional[bytes] = ..., name: _Optional[str] = ..., added_at: _Optional[int] = ..., device_signature: _Optional[bytes] = ...) -> None: ...

class DmDeviceList(_message.Message):
    __slots__ = ("identity", "seq", "issued_at", "devices", "removed")
    IDENTITY_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    ISSUED_AT_FIELD_NUMBER: _ClassVar[int]
    DEVICES_FIELD_NUMBER: _ClassVar[int]
    REMOVED_FIELD_NUMBER: _ClassVar[int]
    identity: bytes
    seq: int
    issued_at: int
    devices: _containers.RepeatedCompositeFieldContainer[DmDevice]
    removed: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, identity: _Optional[bytes] = ..., seq: _Optional[int] = ..., issued_at: _Optional[int] = ..., devices: _Optional[_Iterable[_Union[DmDevice, _Mapping]]] = ..., removed: _Optional[_Iterable[bytes]] = ...) -> None: ...

class SignedDmDeviceList(_message.Message):
    __slots__ = ("list", "signature", "approver", "approval", "added")
    LIST_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    APPROVER_FIELD_NUMBER: _ClassVar[int]
    APPROVAL_FIELD_NUMBER: _ClassVar[int]
    ADDED_FIELD_NUMBER: _ClassVar[int]
    list: bytes
    signature: bytes
    approver: bytes
    approval: bytes
    added: bytes
    def __init__(self, list: _Optional[bytes] = ..., signature: _Optional[bytes] = ..., approver: _Optional[bytes] = ..., approval: _Optional[bytes] = ..., added: _Optional[bytes] = ...) -> None: ...

class OneTimeKey(_message.Message):
    __slots__ = ("curve25519", "key_id", "key", "fallback", "signature")
    CURVE25519_FIELD_NUMBER: _ClassVar[int]
    KEY_ID_FIELD_NUMBER: _ClassVar[int]
    KEY_FIELD_NUMBER: _ClassVar[int]
    FALLBACK_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    curve25519: bytes
    key_id: str
    key: bytes
    fallback: bool
    signature: bytes
    def __init__(self, curve25519: _Optional[bytes] = ..., key_id: _Optional[str] = ..., key: _Optional[bytes] = ..., fallback: _Optional[bool] = ..., signature: _Optional[bytes] = ...) -> None: ...

class ContactCard(_message.Message):
    __slots__ = ("identity", "member", "name", "devices", "fallback_keys", "inboxes", "issued_at")
    IDENTITY_FIELD_NUMBER: _ClassVar[int]
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    DEVICES_FIELD_NUMBER: _ClassVar[int]
    FALLBACK_KEYS_FIELD_NUMBER: _ClassVar[int]
    INBOXES_FIELD_NUMBER: _ClassVar[int]
    ISSUED_AT_FIELD_NUMBER: _ClassVar[int]
    identity: bytes
    member: bytes
    name: str
    devices: SignedDmDeviceList
    fallback_keys: _containers.RepeatedCompositeFieldContainer[OneTimeKey]
    inboxes: _containers.RepeatedCompositeFieldContainer[DeviceInbox]
    issued_at: int
    def __init__(self, identity: _Optional[bytes] = ..., member: _Optional[bytes] = ..., name: _Optional[str] = ..., devices: _Optional[_Union[SignedDmDeviceList, _Mapping]] = ..., fallback_keys: _Optional[_Iterable[_Union[OneTimeKey, _Mapping]]] = ..., inboxes: _Optional[_Iterable[_Union[DeviceInbox, _Mapping]]] = ..., issued_at: _Optional[int] = ...) -> None: ...

class DeviceInbox(_message.Message):
    __slots__ = ("curve25519", "inbox")
    CURVE25519_FIELD_NUMBER: _ClassVar[int]
    INBOX_FIELD_NUMBER: _ClassVar[int]
    curve25519: bytes
    inbox: bytes
    def __init__(self, curve25519: _Optional[bytes] = ..., inbox: _Optional[bytes] = ...) -> None: ...

class SignedContactCard(_message.Message):
    __slots__ = ("card", "signature")
    CARD_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    card: bytes
    signature: bytes
    def __init__(self, card: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class GroupMember(_message.Message):
    __slots__ = ("identity", "name", "community", "member", "joined_seq")
    IDENTITY_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    JOINED_SEQ_FIELD_NUMBER: _ClassVar[int]
    identity: bytes
    name: str
    community: str
    member: bytes
    joined_seq: int
    def __init__(self, identity: _Optional[bytes] = ..., name: _Optional[str] = ..., community: _Optional[str] = ..., member: _Optional[bytes] = ..., joined_seq: _Optional[int] = ...) -> None: ...

class GroupState(_message.Message):
    __slots__ = ("group_id", "seq", "prev", "owner", "members", "invited", "name", "icon", "members_can_add", "allow_attachments", "direct", "relay", "topic", "at")
    GROUP_ID_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    PREV_FIELD_NUMBER: _ClassVar[int]
    OWNER_FIELD_NUMBER: _ClassVar[int]
    MEMBERS_FIELD_NUMBER: _ClassVar[int]
    INVITED_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    ICON_FIELD_NUMBER: _ClassVar[int]
    MEMBERS_CAN_ADD_FIELD_NUMBER: _ClassVar[int]
    ALLOW_ATTACHMENTS_FIELD_NUMBER: _ClassVar[int]
    DIRECT_FIELD_NUMBER: _ClassVar[int]
    RELAY_FIELD_NUMBER: _ClassVar[int]
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    group_id: bytes
    seq: int
    prev: bytes
    owner: bytes
    members: _containers.RepeatedCompositeFieldContainer[GroupMember]
    invited: _containers.RepeatedCompositeFieldContainer[GroupMember]
    name: str
    icon: DmFileRef
    members_can_add: bool
    allow_attachments: bool
    direct: bool
    relay: str
    topic: bytes
    at: int
    def __init__(self, group_id: _Optional[bytes] = ..., seq: _Optional[int] = ..., prev: _Optional[bytes] = ..., owner: _Optional[bytes] = ..., members: _Optional[_Iterable[_Union[GroupMember, _Mapping]]] = ..., invited: _Optional[_Iterable[_Union[GroupMember, _Mapping]]] = ..., name: _Optional[str] = ..., icon: _Optional[_Union[DmFileRef, _Mapping]] = ..., members_can_add: _Optional[bool] = ..., allow_attachments: _Optional[bool] = ..., direct: _Optional[bool] = ..., relay: _Optional[str] = ..., topic: _Optional[bytes] = ..., at: _Optional[int] = ...) -> None: ...

class SignedGroupState(_message.Message):
    __slots__ = ("state", "signer", "signature")
    STATE_FIELD_NUMBER: _ClassVar[int]
    SIGNER_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    state: bytes
    signer: bytes
    signature: bytes
    def __init__(self, state: _Optional[bytes] = ..., signer: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class DmFileRef(_message.Message):
    __slots__ = ("hash", "key", "name", "mime", "size", "ciphertext_size", "width", "height", "thumbnail", "live_chunks")
    HASH_FIELD_NUMBER: _ClassVar[int]
    KEY_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    MIME_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    CIPHERTEXT_SIZE_FIELD_NUMBER: _ClassVar[int]
    WIDTH_FIELD_NUMBER: _ClassVar[int]
    HEIGHT_FIELD_NUMBER: _ClassVar[int]
    THUMBNAIL_FIELD_NUMBER: _ClassVar[int]
    LIVE_CHUNKS_FIELD_NUMBER: _ClassVar[int]
    hash: bytes
    key: bytes
    name: str
    mime: str
    size: int
    ciphertext_size: int
    width: int
    height: int
    thumbnail: bytes
    live_chunks: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, hash: _Optional[bytes] = ..., key: _Optional[bytes] = ..., name: _Optional[str] = ..., mime: _Optional[str] = ..., size: _Optional[int] = ..., ciphertext_size: _Optional[int] = ..., width: _Optional[int] = ..., height: _Optional[int] = ..., thumbnail: _Optional[bytes] = ..., live_chunks: _Optional[_Iterable[bytes]] = ...) -> None: ...

class DmFileStored(_message.Message):
    __slots__ = ("hash", "nodes", "tokens", "expires_at")
    HASH_FIELD_NUMBER: _ClassVar[int]
    NODES_FIELD_NUMBER: _ClassVar[int]
    TOKENS_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    hash: bytes
    nodes: _containers.RepeatedScalarFieldContainer[str]
    tokens: _containers.RepeatedCompositeFieldContainer[DeviceToken]
    expires_at: int
    def __init__(self, hash: _Optional[bytes] = ..., nodes: _Optional[_Iterable[str]] = ..., tokens: _Optional[_Iterable[_Union[DeviceToken, _Mapping]]] = ..., expires_at: _Optional[int] = ...) -> None: ...

class DeviceToken(_message.Message):
    __slots__ = ("curve25519", "token")
    CURVE25519_FIELD_NUMBER: _ClassVar[int]
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    curve25519: bytes
    token: bytes
    def __init__(self, curve25519: _Optional[bytes] = ..., token: _Optional[bytes] = ...) -> None: ...

class DmFileReceived(_message.Message):
    __slots__ = ("hashes",)
    HASHES_FIELD_NUMBER: _ClassVar[int]
    hashes: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, hashes: _Optional[_Iterable[bytes]] = ...) -> None: ...

class DmFileChunk(_message.Message):
    __slots__ = ("hash", "index", "total", "data")
    HASH_FIELD_NUMBER: _ClassVar[int]
    INDEX_FIELD_NUMBER: _ClassVar[int]
    TOTAL_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    hash: bytes
    index: int
    total: int
    data: bytes
    def __init__(self, hash: _Optional[bytes] = ..., index: _Optional[int] = ..., total: _Optional[int] = ..., data: _Optional[bytes] = ...) -> None: ...

class DmEvent(_message.Message):
    __slots__ = ("id", "at", "group_id", "sender", "sender_device", "text", "edit", "delete", "reaction", "typing", "pin", "state", "inboxes", "card", "sync", "stored", "received", "status", "call", "state_tail")
    ID_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    GROUP_ID_FIELD_NUMBER: _ClassVar[int]
    SENDER_FIELD_NUMBER: _ClassVar[int]
    SENDER_DEVICE_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    EDIT_FIELD_NUMBER: _ClassVar[int]
    DELETE_FIELD_NUMBER: _ClassVar[int]
    REACTION_FIELD_NUMBER: _ClassVar[int]
    TYPING_FIELD_NUMBER: _ClassVar[int]
    PIN_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    INBOXES_FIELD_NUMBER: _ClassVar[int]
    CARD_FIELD_NUMBER: _ClassVar[int]
    SYNC_FIELD_NUMBER: _ClassVar[int]
    STORED_FIELD_NUMBER: _ClassVar[int]
    RECEIVED_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    CALL_FIELD_NUMBER: _ClassVar[int]
    STATE_TAIL_FIELD_NUMBER: _ClassVar[int]
    id: bytes
    at: int
    group_id: bytes
    sender: bytes
    sender_device: bytes
    text: DmText
    edit: DmEdit
    delete: DmDelete
    reaction: DmReaction
    typing: DmTyping
    pin: DmPin
    state: SignedGroupState
    inboxes: DmInboxes
    card: SignedContactCard
    sync: bytes
    stored: DmFileStored
    received: DmFileReceived
    status: DmStatus
    call: DmCall
    state_tail: _containers.RepeatedCompositeFieldContainer[SignedGroupState]
    def __init__(self, id: _Optional[bytes] = ..., at: _Optional[int] = ..., group_id: _Optional[bytes] = ..., sender: _Optional[bytes] = ..., sender_device: _Optional[bytes] = ..., text: _Optional[_Union[DmText, _Mapping]] = ..., edit: _Optional[_Union[DmEdit, _Mapping]] = ..., delete: _Optional[_Union[DmDelete, _Mapping]] = ..., reaction: _Optional[_Union[DmReaction, _Mapping]] = ..., typing: _Optional[_Union[DmTyping, _Mapping]] = ..., pin: _Optional[_Union[DmPin, _Mapping]] = ..., state: _Optional[_Union[SignedGroupState, _Mapping]] = ..., inboxes: _Optional[_Union[DmInboxes, _Mapping]] = ..., card: _Optional[_Union[SignedContactCard, _Mapping]] = ..., sync: _Optional[bytes] = ..., stored: _Optional[_Union[DmFileStored, _Mapping]] = ..., received: _Optional[_Union[DmFileReceived, _Mapping]] = ..., status: _Optional[_Union[DmStatus, _Mapping]] = ..., call: _Optional[_Union[DmCall, _Mapping]] = ..., state_tail: _Optional[_Iterable[_Union[SignedGroupState, _Mapping]]] = ...) -> None: ...

class DmText(_message.Message):
    __slots__ = ("text", "reply_to", "files", "mentions")
    TEXT_FIELD_NUMBER: _ClassVar[int]
    REPLY_TO_FIELD_NUMBER: _ClassVar[int]
    FILES_FIELD_NUMBER: _ClassVar[int]
    MENTIONS_FIELD_NUMBER: _ClassVar[int]
    text: str
    reply_to: bytes
    files: _containers.RepeatedCompositeFieldContainer[DmFileRef]
    mentions: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, text: _Optional[str] = ..., reply_to: _Optional[bytes] = ..., files: _Optional[_Iterable[_Union[DmFileRef, _Mapping]]] = ..., mentions: _Optional[_Iterable[bytes]] = ...) -> None: ...

class DmEdit(_message.Message):
    __slots__ = ("target", "text")
    TARGET_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    target: bytes
    text: str
    def __init__(self, target: _Optional[bytes] = ..., text: _Optional[str] = ...) -> None: ...

class DmDelete(_message.Message):
    __slots__ = ("target",)
    TARGET_FIELD_NUMBER: _ClassVar[int]
    target: bytes
    def __init__(self, target: _Optional[bytes] = ...) -> None: ...

class DmReaction(_message.Message):
    __slots__ = ("target", "emoji", "remove")
    TARGET_FIELD_NUMBER: _ClassVar[int]
    EMOJI_FIELD_NUMBER: _ClassVar[int]
    REMOVE_FIELD_NUMBER: _ClassVar[int]
    target: bytes
    emoji: str
    remove: bool
    def __init__(self, target: _Optional[bytes] = ..., emoji: _Optional[str] = ..., remove: _Optional[bool] = ...) -> None: ...

class DmTyping(_message.Message):
    __slots__ = ("typing",)
    TYPING_FIELD_NUMBER: _ClassVar[int]
    typing: bool
    def __init__(self, typing: _Optional[bool] = ...) -> None: ...

class DmPin(_message.Message):
    __slots__ = ("target", "unpin")
    TARGET_FIELD_NUMBER: _ClassVar[int]
    UNPIN_FIELD_NUMBER: _ClassVar[int]
    target: bytes
    unpin: bool
    def __init__(self, target: _Optional[bytes] = ..., unpin: _Optional[bool] = ...) -> None: ...

class DmInboxes(_message.Message):
    __slots__ = ("curve25519", "inbox")
    CURVE25519_FIELD_NUMBER: _ClassVar[int]
    INBOX_FIELD_NUMBER: _ClassVar[int]
    curve25519: bytes
    inbox: bytes
    def __init__(self, curve25519: _Optional[bytes] = ..., inbox: _Optional[bytes] = ...) -> None: ...

class OlmPayload(_message.Message):
    __slots__ = ("sender_identity", "sender_ed25519", "recipient_identity", "recipient_ed25519", "at", "sender_card", "room_key", "invite", "card", "sync", "dummy", "state_update", "status_key")
    SENDER_IDENTITY_FIELD_NUMBER: _ClassVar[int]
    SENDER_ED25519_FIELD_NUMBER: _ClassVar[int]
    RECIPIENT_IDENTITY_FIELD_NUMBER: _ClassVar[int]
    RECIPIENT_ED25519_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    SENDER_CARD_FIELD_NUMBER: _ClassVar[int]
    ROOM_KEY_FIELD_NUMBER: _ClassVar[int]
    INVITE_FIELD_NUMBER: _ClassVar[int]
    CARD_FIELD_NUMBER: _ClassVar[int]
    SYNC_FIELD_NUMBER: _ClassVar[int]
    DUMMY_FIELD_NUMBER: _ClassVar[int]
    STATE_UPDATE_FIELD_NUMBER: _ClassVar[int]
    STATUS_KEY_FIELD_NUMBER: _ClassVar[int]
    sender_identity: bytes
    sender_ed25519: bytes
    recipient_identity: bytes
    recipient_ed25519: bytes
    at: int
    sender_card: SignedContactCard
    room_key: RoomKey
    invite: GroupInvite
    card: SignedContactCard
    sync: bytes
    dummy: OlmDummy
    state_update: SignedGroupState
    status_key: StatusKey
    def __init__(self, sender_identity: _Optional[bytes] = ..., sender_ed25519: _Optional[bytes] = ..., recipient_identity: _Optional[bytes] = ..., recipient_ed25519: _Optional[bytes] = ..., at: _Optional[int] = ..., sender_card: _Optional[_Union[SignedContactCard, _Mapping]] = ..., room_key: _Optional[_Union[RoomKey, _Mapping]] = ..., invite: _Optional[_Union[GroupInvite, _Mapping]] = ..., card: _Optional[_Union[SignedContactCard, _Mapping]] = ..., sync: _Optional[bytes] = ..., dummy: _Optional[_Union[OlmDummy, _Mapping]] = ..., state_update: _Optional[_Union[SignedGroupState, _Mapping]] = ..., status_key: _Optional[_Union[StatusKey, _Mapping]] = ...) -> None: ...

class RoomKey(_message.Message):
    __slots__ = ("group_id", "session_id", "session_key")
    GROUP_ID_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    SESSION_KEY_FIELD_NUMBER: _ClassVar[int]
    group_id: bytes
    session_id: str
    session_key: str
    def __init__(self, group_id: _Optional[bytes] = ..., session_id: _Optional[str] = ..., session_key: _Optional[str] = ...) -> None: ...

class GroupInvite(_message.Message):
    __slots__ = ("chain", "cards")
    CHAIN_FIELD_NUMBER: _ClassVar[int]
    CARDS_FIELD_NUMBER: _ClassVar[int]
    chain: _containers.RepeatedCompositeFieldContainer[SignedGroupState]
    cards: _containers.RepeatedCompositeFieldContainer[SignedContactCard]
    def __init__(self, chain: _Optional[_Iterable[_Union[SignedGroupState, _Mapping]]] = ..., cards: _Optional[_Iterable[_Union[SignedContactCard, _Mapping]]] = ...) -> None: ...

class DmStatus(_message.Message):
    __slots__ = ("state", "text", "emoji", "expires_at", "fresh_until")
    STATE_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    EMOJI_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    FRESH_UNTIL_FIELD_NUMBER: _ClassVar[int]
    state: StatusState
    text: str
    emoji: str
    expires_at: int
    fresh_until: int
    def __init__(self, state: _Optional[_Union[StatusState, str]] = ..., text: _Optional[str] = ..., emoji: _Optional[str] = ..., expires_at: _Optional[int] = ..., fresh_until: _Optional[int] = ...) -> None: ...

class StatusKey(_message.Message):
    __slots__ = ("audience", "session_id", "session_key")
    AUDIENCE_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    SESSION_KEY_FIELD_NUMBER: _ClassVar[int]
    audience: bytes
    session_id: str
    session_key: str
    def __init__(self, audience: _Optional[bytes] = ..., session_id: _Optional[str] = ..., session_key: _Optional[str] = ...) -> None: ...

class StatusDevice(_message.Message):
    __slots__ = ("member", "curve25519", "ed25519", "fallback_key", "issued_at")
    MEMBER_FIELD_NUMBER: _ClassVar[int]
    CURVE25519_FIELD_NUMBER: _ClassVar[int]
    ED25519_FIELD_NUMBER: _ClassVar[int]
    FALLBACK_KEY_FIELD_NUMBER: _ClassVar[int]
    ISSUED_AT_FIELD_NUMBER: _ClassVar[int]
    member: bytes
    curve25519: bytes
    ed25519: bytes
    fallback_key: OneTimeKey
    issued_at: int
    def __init__(self, member: _Optional[bytes] = ..., curve25519: _Optional[bytes] = ..., ed25519: _Optional[bytes] = ..., fallback_key: _Optional[_Union[OneTimeKey, _Mapping]] = ..., issued_at: _Optional[int] = ...) -> None: ...

class SignedStatusDevice(_message.Message):
    __slots__ = ("device", "signature")
    DEVICE_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    device: bytes
    signature: bytes
    def __init__(self, device: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class OlmDummy(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class Envelope(_message.Message):
    __slots__ = ("olm", "megolm")
    OLM_FIELD_NUMBER: _ClassVar[int]
    MEGOLM_FIELD_NUMBER: _ClassVar[int]
    olm: OlmEnvelope
    megolm: MegolmEnvelope
    def __init__(self, olm: _Optional[_Union[OlmEnvelope, _Mapping]] = ..., megolm: _Optional[_Union[MegolmEnvelope, _Mapping]] = ...) -> None: ...

class OlmEnvelope(_message.Message):
    __slots__ = ("sender_curve25519", "recipient_curve25519", "type", "ciphertext")
    SENDER_CURVE25519_FIELD_NUMBER: _ClassVar[int]
    RECIPIENT_CURVE25519_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    CIPHERTEXT_FIELD_NUMBER: _ClassVar[int]
    sender_curve25519: bytes
    recipient_curve25519: bytes
    type: int
    ciphertext: bytes
    def __init__(self, sender_curve25519: _Optional[bytes] = ..., recipient_curve25519: _Optional[bytes] = ..., type: _Optional[int] = ..., ciphertext: _Optional[bytes] = ...) -> None: ...

class MegolmEnvelope(_message.Message):
    __slots__ = ("sender_curve25519", "session_id", "ciphertext")
    SENDER_CURVE25519_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    CIPHERTEXT_FIELD_NUMBER: _ClassVar[int]
    sender_curve25519: bytes
    session_id: str
    ciphertext: bytes
    def __init__(self, sender_curve25519: _Optional[bytes] = ..., session_id: _Optional[str] = ..., ciphertext: _Optional[bytes] = ...) -> None: ...

class DmCall(_message.Message):
    __slots__ = ("kind", "call_id", "call_nonce", "signing_seed", "call_key", "relay", "participant", "slot", "self_mute", "self_deaf", "camera", "screen", "video")
    KIND_FIELD_NUMBER: _ClassVar[int]
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    CALL_NONCE_FIELD_NUMBER: _ClassVar[int]
    SIGNING_SEED_FIELD_NUMBER: _ClassVar[int]
    CALL_KEY_FIELD_NUMBER: _ClassVar[int]
    RELAY_FIELD_NUMBER: _ClassVar[int]
    PARTICIPANT_FIELD_NUMBER: _ClassVar[int]
    SLOT_FIELD_NUMBER: _ClassVar[int]
    SELF_MUTE_FIELD_NUMBER: _ClassVar[int]
    SELF_DEAF_FIELD_NUMBER: _ClassVar[int]
    CAMERA_FIELD_NUMBER: _ClassVar[int]
    SCREEN_FIELD_NUMBER: _ClassVar[int]
    VIDEO_FIELD_NUMBER: _ClassVar[int]
    kind: DmCallKind
    call_id: bytes
    call_nonce: bytes
    signing_seed: bytes
    call_key: bytes
    relay: str
    participant: bytes
    slot: int
    self_mute: bool
    self_deaf: bool
    camera: bool
    screen: bool
    video: bool
    def __init__(self, kind: _Optional[_Union[DmCallKind, str]] = ..., call_id: _Optional[bytes] = ..., call_nonce: _Optional[bytes] = ..., signing_seed: _Optional[bytes] = ..., call_key: _Optional[bytes] = ..., relay: _Optional[str] = ..., participant: _Optional[bytes] = ..., slot: _Optional[int] = ..., self_mute: _Optional[bool] = ..., self_deaf: _Optional[bool] = ..., camera: _Optional[bool] = ..., screen: _Optional[bool] = ..., video: _Optional[bool] = ...) -> None: ...
