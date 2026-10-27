from openarcanum.wire import community_pb2 as _community_pb2
from openarcanum.wire import session_pb2 as _session_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class CommunityState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    COMMUNITY_STATE_UNSPECIFIED: _ClassVar[CommunityState]
    COMMUNITY_STATE_ONLINE: _ClassVar[CommunityState]
    COMMUNITY_STATE_DEGRADED: _ClassVar[CommunityState]
    COMMUNITY_STATE_OFFLINE: _ClassVar[CommunityState]
    COMMUNITY_STATE_TERMINATED: _ClassVar[CommunityState]
COMMUNITY_STATE_UNSPECIFIED: CommunityState
COMMUNITY_STATE_ONLINE: CommunityState
COMMUNITY_STATE_DEGRADED: CommunityState
COMMUNITY_STATE_OFFLINE: CommunityState
COMMUNITY_STATE_TERMINATED: CommunityState

class Route(_message.Message):
    __slots__ = ("route_id", "server_key", "relays", "seq", "published_at", "reregistered_at")
    ROUTE_ID_FIELD_NUMBER: _ClassVar[int]
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    RELAYS_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    PUBLISHED_AT_FIELD_NUMBER: _ClassVar[int]
    REREGISTERED_AT_FIELD_NUMBER: _ClassVar[int]
    route_id: bytes
    server_key: bytes
    relays: _containers.RepeatedScalarFieldContainer[str]
    seq: int
    published_at: int
    reregistered_at: int
    def __init__(self, route_id: _Optional[bytes] = ..., server_key: _Optional[bytes] = ..., relays: _Optional[_Iterable[str]] = ..., seq: _Optional[int] = ..., published_at: _Optional[int] = ..., reregistered_at: _Optional[int] = ...) -> None: ...

class SignedRoute(_message.Message):
    __slots__ = ("route", "signature", "cert")
    ROUTE_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    CERT_FIELD_NUMBER: _ClassVar[int]
    route: bytes
    signature: bytes
    cert: _session_pb2.SignedServerCert
    def __init__(self, route: _Optional[bytes] = ..., signature: _Optional[bytes] = ..., cert: _Optional[_Union[_session_pb2.SignedServerCert, _Mapping]] = ...) -> None: ...

class Listing(_message.Message):
    __slots__ = ("community", "server_key", "route_id", "name", "description", "tags", "language", "member_range", "join_policy", "public_join_key", "relays", "seq", "published_at", "withdrawn", "icon_sha256")
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    ROUTE_ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    TAGS_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    MEMBER_RANGE_FIELD_NUMBER: _ClassVar[int]
    JOIN_POLICY_FIELD_NUMBER: _ClassVar[int]
    PUBLIC_JOIN_KEY_FIELD_NUMBER: _ClassVar[int]
    RELAYS_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    PUBLISHED_AT_FIELD_NUMBER: _ClassVar[int]
    WITHDRAWN_FIELD_NUMBER: _ClassVar[int]
    ICON_SHA256_FIELD_NUMBER: _ClassVar[int]
    community: bytes
    server_key: bytes
    route_id: bytes
    name: str
    description: str
    tags: _containers.RepeatedScalarFieldContainer[str]
    language: str
    member_range: str
    join_policy: _community_pb2.JoinPolicy
    public_join_key: str
    relays: _containers.RepeatedScalarFieldContainer[str]
    seq: int
    published_at: int
    withdrawn: bool
    icon_sha256: bytes
    def __init__(self, community: _Optional[bytes] = ..., server_key: _Optional[bytes] = ..., route_id: _Optional[bytes] = ..., name: _Optional[str] = ..., description: _Optional[str] = ..., tags: _Optional[_Iterable[str]] = ..., language: _Optional[str] = ..., member_range: _Optional[str] = ..., join_policy: _Optional[_Union[_community_pb2.JoinPolicy, str]] = ..., public_join_key: _Optional[str] = ..., relays: _Optional[_Iterable[str]] = ..., seq: _Optional[int] = ..., published_at: _Optional[int] = ..., withdrawn: _Optional[bool] = ..., icon_sha256: _Optional[bytes] = ...) -> None: ...

class SignedListing(_message.Message):
    __slots__ = ("listing", "signature", "cert", "icon")
    LISTING_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    CERT_FIELD_NUMBER: _ClassVar[int]
    ICON_FIELD_NUMBER: _ClassVar[int]
    listing: bytes
    signature: bytes
    cert: _session_pb2.SignedServerCert
    icon: bytes
    def __init__(self, listing: _Optional[bytes] = ..., signature: _Optional[bytes] = ..., cert: _Optional[_Union[_session_pb2.SignedServerCert, _Mapping]] = ..., icon: _Optional[bytes] = ...) -> None: ...

class SearchResponse(_message.Message):
    __slots__ = ("listings", "statuses")
    LISTINGS_FIELD_NUMBER: _ClassVar[int]
    STATUSES_FIELD_NUMBER: _ClassVar[int]
    listings: _containers.RepeatedCompositeFieldContainer[SignedListing]
    statuses: _containers.RepeatedCompositeFieldContainer[CommunityStatus]
    def __init__(self, listings: _Optional[_Iterable[_Union[SignedListing, _Mapping]]] = ..., statuses: _Optional[_Iterable[_Union[CommunityStatus, _Mapping]]] = ...) -> None: ...

class DirectorySnapshot(_message.Message):
    __slots__ = ("listings", "next", "more", "total", "generated_at")
    LISTINGS_FIELD_NUMBER: _ClassVar[int]
    NEXT_FIELD_NUMBER: _ClassVar[int]
    MORE_FIELD_NUMBER: _ClassVar[int]
    TOTAL_FIELD_NUMBER: _ClassVar[int]
    GENERATED_AT_FIELD_NUMBER: _ClassVar[int]
    listings: _containers.RepeatedCompositeFieldContainer[SignedListing]
    next: int
    more: bool
    total: int
    generated_at: int
    def __init__(self, listings: _Optional[_Iterable[_Union[SignedListing, _Mapping]]] = ..., next: _Optional[int] = ..., more: _Optional[bool] = ..., total: _Optional[int] = ..., generated_at: _Optional[int] = ...) -> None: ...

class RelayHeartbeat(_message.Message):
    __slots__ = ("url", "region", "capacity", "connections", "relay_key", "at")
    URL_FIELD_NUMBER: _ClassVar[int]
    REGION_FIELD_NUMBER: _ClassVar[int]
    CAPACITY_FIELD_NUMBER: _ClassVar[int]
    CONNECTIONS_FIELD_NUMBER: _ClassVar[int]
    RELAY_KEY_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    url: str
    region: str
    capacity: int
    connections: int
    relay_key: bytes
    at: int
    def __init__(self, url: _Optional[str] = ..., region: _Optional[str] = ..., capacity: _Optional[int] = ..., connections: _Optional[int] = ..., relay_key: _Optional[bytes] = ..., at: _Optional[int] = ...) -> None: ...

class SignedHeartbeat(_message.Message):
    __slots__ = ("heartbeat", "signature")
    HEARTBEAT_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    heartbeat: bytes
    signature: bytes
    def __init__(self, heartbeat: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class RelayInfo(_message.Message):
    __slots__ = ("url", "region", "capacity", "connections", "last_seen")
    URL_FIELD_NUMBER: _ClassVar[int]
    REGION_FIELD_NUMBER: _ClassVar[int]
    CAPACITY_FIELD_NUMBER: _ClassVar[int]
    CONNECTIONS_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_FIELD_NUMBER: _ClassVar[int]
    url: str
    region: str
    capacity: int
    connections: int
    last_seen: int
    def __init__(self, url: _Optional[str] = ..., region: _Optional[str] = ..., capacity: _Optional[int] = ..., connections: _Optional[int] = ..., last_seen: _Optional[int] = ...) -> None: ...

class RelayList(_message.Message):
    __slots__ = ("relays",)
    RELAYS_FIELD_NUMBER: _ClassVar[int]
    relays: _containers.RepeatedCompositeFieldContainer[RelayInfo]
    def __init__(self, relays: _Optional[_Iterable[_Union[RelayInfo, _Mapping]]] = ...) -> None: ...

class CommunityHeartbeat(_message.Message):
    __slots__ = ("route_id", "server_key", "at", "stopping", "relays_attached", "relays_wanted", "pinned")
    ROUTE_ID_FIELD_NUMBER: _ClassVar[int]
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    STOPPING_FIELD_NUMBER: _ClassVar[int]
    RELAYS_ATTACHED_FIELD_NUMBER: _ClassVar[int]
    RELAYS_WANTED_FIELD_NUMBER: _ClassVar[int]
    PINNED_FIELD_NUMBER: _ClassVar[int]
    route_id: bytes
    server_key: bytes
    at: int
    stopping: bool
    relays_attached: int
    relays_wanted: int
    pinned: bool
    def __init__(self, route_id: _Optional[bytes] = ..., server_key: _Optional[bytes] = ..., at: _Optional[int] = ..., stopping: _Optional[bool] = ..., relays_attached: _Optional[int] = ..., relays_wanted: _Optional[int] = ..., pinned: _Optional[bool] = ...) -> None: ...

class SignedCommunityHeartbeat(_message.Message):
    __slots__ = ("heartbeat", "signature")
    HEARTBEAT_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    heartbeat: bytes
    signature: bytes
    def __init__(self, heartbeat: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class CommunityStatus(_message.Message):
    __slots__ = ("route_id", "state", "last_seen", "reaped_at", "heartbeat", "reap_after_seconds")
    ROUTE_ID_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_FIELD_NUMBER: _ClassVar[int]
    REAPED_AT_FIELD_NUMBER: _ClassVar[int]
    HEARTBEAT_FIELD_NUMBER: _ClassVar[int]
    REAP_AFTER_SECONDS_FIELD_NUMBER: _ClassVar[int]
    route_id: bytes
    state: CommunityState
    last_seen: int
    reaped_at: int
    heartbeat: SignedCommunityHeartbeat
    reap_after_seconds: int
    def __init__(self, route_id: _Optional[bytes] = ..., state: _Optional[_Union[CommunityState, str]] = ..., last_seen: _Optional[int] = ..., reaped_at: _Optional[int] = ..., heartbeat: _Optional[_Union[SignedCommunityHeartbeat, _Mapping]] = ..., reap_after_seconds: _Optional[int] = ...) -> None: ...

class MailboxDrop(_message.Message):
    __slots__ = ("inbox", "blob")
    INBOX_FIELD_NUMBER: _ClassVar[int]
    BLOB_FIELD_NUMBER: _ClassVar[int]
    inbox: bytes
    blob: bytes
    def __init__(self, inbox: _Optional[bytes] = ..., blob: _Optional[bytes] = ...) -> None: ...

class MailboxFetch(_message.Message):
    __slots__ = ("token", "after_id")
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    AFTER_ID_FIELD_NUMBER: _ClassVar[int]
    token: bytes
    after_id: int
    def __init__(self, token: _Optional[bytes] = ..., after_id: _Optional[int] = ...) -> None: ...

class MailboxItem(_message.Message):
    __slots__ = ("id", "blob", "received_at", "gid")
    ID_FIELD_NUMBER: _ClassVar[int]
    BLOB_FIELD_NUMBER: _ClassVar[int]
    RECEIVED_AT_FIELD_NUMBER: _ClassVar[int]
    GID_FIELD_NUMBER: _ClassVar[int]
    id: int
    blob: bytes
    received_at: int
    gid: bytes
    def __init__(self, id: _Optional[int] = ..., blob: _Optional[bytes] = ..., received_at: _Optional[int] = ..., gid: _Optional[bytes] = ...) -> None: ...

class MailboxItems(_message.Message):
    __slots__ = ("items", "used_bytes", "quota_bytes", "epoch")
    ITEMS_FIELD_NUMBER: _ClassVar[int]
    USED_BYTES_FIELD_NUMBER: _ClassVar[int]
    QUOTA_BYTES_FIELD_NUMBER: _ClassVar[int]
    EPOCH_FIELD_NUMBER: _ClassVar[int]
    items: _containers.RepeatedCompositeFieldContainer[MailboxItem]
    used_bytes: int
    quota_bytes: int
    epoch: int
    def __init__(self, items: _Optional[_Iterable[_Union[MailboxItem, _Mapping]]] = ..., used_bytes: _Optional[int] = ..., quota_bytes: _Optional[int] = ..., epoch: _Optional[int] = ...) -> None: ...

class MailboxDelete(_message.Message):
    __slots__ = ("token", "ids", "gids")
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    IDS_FIELD_NUMBER: _ClassVar[int]
    GIDS_FIELD_NUMBER: _ClassVar[int]
    token: bytes
    ids: _containers.RepeatedScalarFieldContainer[int]
    gids: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, token: _Optional[bytes] = ..., ids: _Optional[_Iterable[int]] = ..., gids: _Optional[_Iterable[bytes]] = ...) -> None: ...

class ContactCardPut(_message.Message):
    __slots__ = ("token", "card")
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    CARD_FIELD_NUMBER: _ClassVar[int]
    token: bytes
    card: bytes
    def __init__(self, token: _Optional[bytes] = ..., card: _Optional[bytes] = ...) -> None: ...

class ContactCardDelete(_message.Message):
    __slots__ = ("token",)
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    token: bytes
    def __init__(self, token: _Optional[bytes] = ...) -> None: ...

class ContactCardFetched(_message.Message):
    __slots__ = ("card", "updated_at")
    CARD_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    card: bytes
    updated_at: int
    def __init__(self, card: _Optional[bytes] = ..., updated_at: _Optional[int] = ...) -> None: ...

class NameplateRequest(_message.Message):
    __slots__ = ("relay",)
    RELAY_FIELD_NUMBER: _ClassVar[int]
    relay: str
    def __init__(self, relay: _Optional[str] = ...) -> None: ...

class Nameplate(_message.Message):
    __slots__ = ("number", "topic", "relay", "expires_at")
    NUMBER_FIELD_NUMBER: _ClassVar[int]
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    RELAY_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    number: int
    topic: bytes
    relay: str
    expires_at: int
    def __init__(self, number: _Optional[int] = ..., topic: _Optional[bytes] = ..., relay: _Optional[str] = ..., expires_at: _Optional[int] = ...) -> None: ...

class NameplateClaim(_message.Message):
    __slots__ = ("number",)
    NUMBER_FIELD_NUMBER: _ClassVar[int]
    number: int
    def __init__(self, number: _Optional[int] = ...) -> None: ...

class BlobCreate(_message.Message):
    __slots__ = ("hash", "size", "token_hashes")
    HASH_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    TOKEN_HASHES_FIELD_NUMBER: _ClassVar[int]
    hash: bytes
    size: int
    token_hashes: _containers.RepeatedScalarFieldContainer[bytes]
    def __init__(self, hash: _Optional[bytes] = ..., size: _Optional[int] = ..., token_hashes: _Optional[_Iterable[bytes]] = ...) -> None: ...

class BlobChunk(_message.Message):
    __slots__ = ("hash", "index", "data", "upload")
    HASH_FIELD_NUMBER: _ClassVar[int]
    INDEX_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    UPLOAD_FIELD_NUMBER: _ClassVar[int]
    hash: bytes
    index: int
    data: bytes
    upload: bytes
    def __init__(self, hash: _Optional[bytes] = ..., index: _Optional[int] = ..., data: _Optional[bytes] = ..., upload: _Optional[bytes] = ...) -> None: ...

class BlobUploadStatus(_message.Message):
    __slots__ = ("hash", "size", "chunk_size", "missing", "complete", "expires_at", "upload")
    HASH_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    CHUNK_SIZE_FIELD_NUMBER: _ClassVar[int]
    MISSING_FIELD_NUMBER: _ClassVar[int]
    COMPLETE_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    UPLOAD_FIELD_NUMBER: _ClassVar[int]
    hash: bytes
    size: int
    chunk_size: int
    missing: _containers.RepeatedScalarFieldContainer[int]
    complete: bool
    expires_at: int
    upload: bytes
    def __init__(self, hash: _Optional[bytes] = ..., size: _Optional[int] = ..., chunk_size: _Optional[int] = ..., missing: _Optional[_Iterable[int]] = ..., complete: _Optional[bool] = ..., expires_at: _Optional[int] = ..., upload: _Optional[bytes] = ...) -> None: ...

class BlobFetch(_message.Message):
    __slots__ = ("hash", "token", "index")
    HASH_FIELD_NUMBER: _ClassVar[int]
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    INDEX_FIELD_NUMBER: _ClassVar[int]
    hash: bytes
    token: bytes
    index: int
    def __init__(self, hash: _Optional[bytes] = ..., token: _Optional[bytes] = ..., index: _Optional[int] = ...) -> None: ...

class BlobChunkData(_message.Message):
    __slots__ = ("size", "chunk_size", "index", "data", "expires_at")
    SIZE_FIELD_NUMBER: _ClassVar[int]
    CHUNK_SIZE_FIELD_NUMBER: _ClassVar[int]
    INDEX_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    size: int
    chunk_size: int
    index: int
    data: bytes
    expires_at: int
    def __init__(self, size: _Optional[int] = ..., chunk_size: _Optional[int] = ..., index: _Optional[int] = ..., data: _Optional[bytes] = ..., expires_at: _Optional[int] = ...) -> None: ...

class BlobAck(_message.Message):
    __slots__ = ("hash", "token")
    HASH_FIELD_NUMBER: _ClassVar[int]
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    hash: bytes
    token: bytes
    def __init__(self, hash: _Optional[bytes] = ..., token: _Optional[bytes] = ...) -> None: ...

class ReplicationBatch(_message.Message):
    __slots__ = ("listings", "routes", "heartbeats", "cursor", "community_heartbeats", "epoch", "maintainer_sets")
    LISTINGS_FIELD_NUMBER: _ClassVar[int]
    ROUTES_FIELD_NUMBER: _ClassVar[int]
    HEARTBEATS_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    COMMUNITY_HEARTBEATS_FIELD_NUMBER: _ClassVar[int]
    EPOCH_FIELD_NUMBER: _ClassVar[int]
    MAINTAINER_SETS_FIELD_NUMBER: _ClassVar[int]
    listings: _containers.RepeatedCompositeFieldContainer[SignedListing]
    routes: _containers.RepeatedCompositeFieldContainer[SignedRoute]
    heartbeats: _containers.RepeatedCompositeFieldContainer[SignedHeartbeat]
    cursor: int
    community_heartbeats: _containers.RepeatedCompositeFieldContainer[SignedCommunityHeartbeat]
    epoch: int
    maintainer_sets: _containers.RepeatedCompositeFieldContainer[SignedMaintainerSet]
    def __init__(self, listings: _Optional[_Iterable[_Union[SignedListing, _Mapping]]] = ..., routes: _Optional[_Iterable[_Union[SignedRoute, _Mapping]]] = ..., heartbeats: _Optional[_Iterable[_Union[SignedHeartbeat, _Mapping]]] = ..., cursor: _Optional[int] = ..., community_heartbeats: _Optional[_Iterable[_Union[SignedCommunityHeartbeat, _Mapping]]] = ..., epoch: _Optional[int] = ..., maintainer_sets: _Optional[_Iterable[_Union[SignedMaintainerSet, _Mapping]]] = ...) -> None: ...

class MaintainerNode(_message.Message):
    __slots__ = ("url", "node_key", "role", "region", "no_directory")
    URL_FIELD_NUMBER: _ClassVar[int]
    NODE_KEY_FIELD_NUMBER: _ClassVar[int]
    ROLE_FIELD_NUMBER: _ClassVar[int]
    REGION_FIELD_NUMBER: _ClassVar[int]
    NO_DIRECTORY_FIELD_NUMBER: _ClassVar[int]
    url: str
    node_key: bytes
    role: str
    region: str
    no_directory: bool
    def __init__(self, url: _Optional[str] = ..., node_key: _Optional[bytes] = ..., role: _Optional[str] = ..., region: _Optional[str] = ..., no_directory: _Optional[bool] = ...) -> None: ...

class MaintainerSet(_message.Message):
    __slots__ = ("operator_key", "seq", "issued_at", "expires_at", "nodes")
    OPERATOR_KEY_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    ISSUED_AT_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    NODES_FIELD_NUMBER: _ClassVar[int]
    operator_key: bytes
    seq: int
    issued_at: int
    expires_at: int
    nodes: _containers.RepeatedCompositeFieldContainer[MaintainerNode]
    def __init__(self, operator_key: _Optional[bytes] = ..., seq: _Optional[int] = ..., issued_at: _Optional[int] = ..., expires_at: _Optional[int] = ..., nodes: _Optional[_Iterable[_Union[MaintainerNode, _Mapping]]] = ...) -> None: ...

class SignedMaintainerSet(_message.Message):
    __slots__ = ("set", "signature")
    SET_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    set: bytes
    signature: bytes
    def __init__(self, set: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class NodeInfo(_message.Message):
    __slots__ = ("node_key", "noise_key", "directory")
    NODE_KEY_FIELD_NUMBER: _ClassVar[int]
    NOISE_KEY_FIELD_NUMBER: _ClassVar[int]
    DIRECTORY_FIELD_NUMBER: _ClassVar[int]
    node_key: bytes
    noise_key: bytes
    directory: bool
    def __init__(self, node_key: _Optional[bytes] = ..., noise_key: _Optional[bytes] = ..., directory: _Optional[bool] = ...) -> None: ...

class PeerMessage(_message.Message):
    __slots__ = ("id", "pull", "batch", "poke", "error", "blob_chunk_request", "blob_chunk")
    ID_FIELD_NUMBER: _ClassVar[int]
    PULL_FIELD_NUMBER: _ClassVar[int]
    BATCH_FIELD_NUMBER: _ClassVar[int]
    POKE_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    BLOB_CHUNK_REQUEST_FIELD_NUMBER: _ClassVar[int]
    BLOB_CHUNK_FIELD_NUMBER: _ClassVar[int]
    id: int
    pull: PeerPull
    batch: PeerBatch
    poke: PeerPoke
    error: PeerError
    blob_chunk_request: PeerBlobChunkRequest
    blob_chunk: PeerBlobChunk
    def __init__(self, id: _Optional[int] = ..., pull: _Optional[_Union[PeerPull, _Mapping]] = ..., batch: _Optional[_Union[PeerBatch, _Mapping]] = ..., poke: _Optional[_Union[PeerPoke, _Mapping]] = ..., error: _Optional[_Union[PeerError, _Mapping]] = ..., blob_chunk_request: _Optional[_Union[PeerBlobChunkRequest, _Mapping]] = ..., blob_chunk: _Optional[_Union[PeerBlobChunk, _Mapping]] = ...) -> None: ...

class PeerBlobChunkRequest(_message.Message):
    __slots__ = ("upload_id", "index")
    UPLOAD_ID_FIELD_NUMBER: _ClassVar[int]
    INDEX_FIELD_NUMBER: _ClassVar[int]
    upload_id: bytes
    index: int
    def __init__(self, upload_id: _Optional[bytes] = ..., index: _Optional[int] = ...) -> None: ...

class PeerBlobChunk(_message.Message):
    __slots__ = ("upload_id", "index", "data")
    UPLOAD_ID_FIELD_NUMBER: _ClassVar[int]
    INDEX_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    upload_id: bytes
    index: int
    data: bytes
    def __init__(self, upload_id: _Optional[bytes] = ..., index: _Optional[int] = ..., data: _Optional[bytes] = ...) -> None: ...

class PeerPull(_message.Message):
    __slots__ = ("cursor", "epoch")
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    EPOCH_FIELD_NUMBER: _ClassVar[int]
    cursor: int
    epoch: int
    def __init__(self, cursor: _Optional[int] = ..., epoch: _Optional[int] = ...) -> None: ...

class PeerBatch(_message.Message):
    __slots__ = ("epoch", "cursor", "more", "head", "entries")
    EPOCH_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    MORE_FIELD_NUMBER: _ClassVar[int]
    HEAD_FIELD_NUMBER: _ClassVar[int]
    ENTRIES_FIELD_NUMBER: _ClassVar[int]
    epoch: int
    cursor: int
    more: bool
    head: int
    entries: _containers.RepeatedCompositeFieldContainer[PeerEntry]
    def __init__(self, epoch: _Optional[int] = ..., cursor: _Optional[int] = ..., more: _Optional[bool] = ..., head: _Optional[int] = ..., entries: _Optional[_Iterable[_Union[PeerEntry, _Mapping]]] = ...) -> None: ...

class PeerEntry(_message.Message):
    __slots__ = ("mrev", "item", "tombstone", "blob_meta", "blob_release", "decision", "community_tombstone")
    MREV_FIELD_NUMBER: _ClassVar[int]
    ITEM_FIELD_NUMBER: _ClassVar[int]
    TOMBSTONE_FIELD_NUMBER: _ClassVar[int]
    BLOB_META_FIELD_NUMBER: _ClassVar[int]
    BLOB_RELEASE_FIELD_NUMBER: _ClassVar[int]
    DECISION_FIELD_NUMBER: _ClassVar[int]
    COMMUNITY_TOMBSTONE_FIELD_NUMBER: _ClassVar[int]
    mrev: int
    item: PeerMailboxItem
    tombstone: PeerMailboxTombstone
    blob_meta: PeerBlobMeta
    blob_release: PeerBlobRelease
    decision: PeerDecision
    community_tombstone: PeerCommunityTombstone
    def __init__(self, mrev: _Optional[int] = ..., item: _Optional[_Union[PeerMailboxItem, _Mapping]] = ..., tombstone: _Optional[_Union[PeerMailboxTombstone, _Mapping]] = ..., blob_meta: _Optional[_Union[PeerBlobMeta, _Mapping]] = ..., blob_release: _Optional[_Union[PeerBlobRelease, _Mapping]] = ..., decision: _Optional[_Union[PeerDecision, _Mapping]] = ..., community_tombstone: _Optional[_Union[PeerCommunityTombstone, _Mapping]] = ...) -> None: ...

class PeerCommunityTombstone(_message.Message):
    __slots__ = ("route_id", "community", "last_seen", "reaped_at")
    ROUTE_ID_FIELD_NUMBER: _ClassVar[int]
    COMMUNITY_FIELD_NUMBER: _ClassVar[int]
    LAST_SEEN_FIELD_NUMBER: _ClassVar[int]
    REAPED_AT_FIELD_NUMBER: _ClassVar[int]
    route_id: bytes
    community: bytes
    last_seen: int
    reaped_at: int
    def __init__(self, route_id: _Optional[bytes] = ..., community: _Optional[bytes] = ..., last_seen: _Optional[int] = ..., reaped_at: _Optional[int] = ...) -> None: ...

class PeerDecision(_message.Message):
    __slots__ = ("origin", "kind", "key", "action", "at", "signature")
    ORIGIN_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    KEY_FIELD_NUMBER: _ClassVar[int]
    ACTION_FIELD_NUMBER: _ClassVar[int]
    AT_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    origin: bytes
    kind: str
    key: str
    action: str
    at: int
    signature: bytes
    def __init__(self, origin: _Optional[bytes] = ..., kind: _Optional[str] = ..., key: _Optional[str] = ..., action: _Optional[str] = ..., at: _Optional[int] = ..., signature: _Optional[bytes] = ...) -> None: ...

class PeerBlobMeta(_message.Message):
    __slots__ = ("upload_id", "hash", "size", "token_hashes", "expires_at")
    UPLOAD_ID_FIELD_NUMBER: _ClassVar[int]
    HASH_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    TOKEN_HASHES_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    upload_id: bytes
    hash: bytes
    size: int
    token_hashes: _containers.RepeatedScalarFieldContainer[bytes]
    expires_at: int
    def __init__(self, upload_id: _Optional[bytes] = ..., hash: _Optional[bytes] = ..., size: _Optional[int] = ..., token_hashes: _Optional[_Iterable[bytes]] = ..., expires_at: _Optional[int] = ...) -> None: ...

class PeerBlobRelease(_message.Message):
    __slots__ = ("upload_id", "token_hash", "expires_at")
    UPLOAD_ID_FIELD_NUMBER: _ClassVar[int]
    TOKEN_HASH_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    upload_id: bytes
    token_hash: bytes
    expires_at: int
    def __init__(self, upload_id: _Optional[bytes] = ..., token_hash: _Optional[bytes] = ..., expires_at: _Optional[int] = ...) -> None: ...

class PeerMailboxItem(_message.Message):
    __slots__ = ("inbox", "blob", "received_at", "expires_at")
    INBOX_FIELD_NUMBER: _ClassVar[int]
    BLOB_FIELD_NUMBER: _ClassVar[int]
    RECEIVED_AT_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    inbox: bytes
    blob: bytes
    received_at: int
    expires_at: int
    def __init__(self, inbox: _Optional[bytes] = ..., blob: _Optional[bytes] = ..., received_at: _Optional[int] = ..., expires_at: _Optional[int] = ...) -> None: ...

class PeerMailboxTombstone(_message.Message):
    __slots__ = ("gid", "inbox", "expires_at")
    GID_FIELD_NUMBER: _ClassVar[int]
    INBOX_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    gid: bytes
    inbox: bytes
    expires_at: int
    def __init__(self, gid: _Optional[bytes] = ..., inbox: _Optional[bytes] = ..., expires_at: _Optional[int] = ...) -> None: ...

class PeerPoke(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class PeerError(_message.Message):
    __slots__ = ("message",)
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    message: str
    def __init__(self, message: _Optional[str] = ...) -> None: ...
