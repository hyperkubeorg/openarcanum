from openarcanum.wire import call_pb2 as _call_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ErrorCode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ERROR_CODE_UNSPECIFIED: _ClassVar[ErrorCode]
    ERROR_CODE_BAD_FRAME: _ClassVar[ErrorCode]
    ERROR_CODE_TOO_LARGE: _ClassVar[ErrorCode]
    ERROR_CODE_RATE_LIMITED: _ClassVar[ErrorCode]
    ERROR_CODE_NO_ENDPOINT: _ClassVar[ErrorCode]
    ERROR_CODE_BAD_SIGNATURE: _ClassVar[ErrorCode]
    ERROR_CODE_LIMIT: _ClassVar[ErrorCode]
    ERROR_CODE_SLOW_CONSUMER: _ClassVar[ErrorCode]
    ERROR_CODE_CALL_REFUSED: _ClassVar[ErrorCode]
    ERROR_CODE_NO_CALLS: _ClassVar[ErrorCode]
    ERROR_CODE_UNSUPPORTED_VERSION: _ClassVar[ErrorCode]
    ERROR_CODE_POW_REQUIRED: _ClassVar[ErrorCode]
    ERROR_CODE_FANOUT_TOKEN_REQUIRED: _ClassVar[ErrorCode]
    ERROR_CODE_BAD_TOKEN: _ClassVar[ErrorCode]
    ERROR_CODE_NOT_DEDICATED: _ClassVar[ErrorCode]
    ERROR_CODE_BAD_COMMAND: _ClassVar[ErrorCode]
ERROR_CODE_UNSPECIFIED: ErrorCode
ERROR_CODE_BAD_FRAME: ErrorCode
ERROR_CODE_TOO_LARGE: ErrorCode
ERROR_CODE_RATE_LIMITED: ErrorCode
ERROR_CODE_NO_ENDPOINT: ErrorCode
ERROR_CODE_BAD_SIGNATURE: ErrorCode
ERROR_CODE_LIMIT: ErrorCode
ERROR_CODE_SLOW_CONSUMER: ErrorCode
ERROR_CODE_CALL_REFUSED: ErrorCode
ERROR_CODE_NO_CALLS: ErrorCode
ERROR_CODE_UNSUPPORTED_VERSION: ErrorCode
ERROR_CODE_POW_REQUIRED: ErrorCode
ERROR_CODE_FANOUT_TOKEN_REQUIRED: ErrorCode
ERROR_CODE_BAD_TOKEN: ErrorCode
ERROR_CODE_NOT_DEDICATED: ErrorCode
ERROR_CODE_BAD_COMMAND: ErrorCode

class RelayFrame(_message.Message):
    __slots__ = ("hello", "subscribe", "unsubscribe", "publish", "deliver", "register_endpoint", "open_stream", "stream_opened", "stream_data", "stream_close", "ack", "error", "call_join", "call_answer", "call_signal", "call_leave", "call_layer", "call_update", "call_state", "start", "fanout_auth", "moderation", "moderation_result", "call_listen", "call_watchers")
    HELLO_FIELD_NUMBER: _ClassVar[int]
    SUBSCRIBE_FIELD_NUMBER: _ClassVar[int]
    UNSUBSCRIBE_FIELD_NUMBER: _ClassVar[int]
    PUBLISH_FIELD_NUMBER: _ClassVar[int]
    DELIVER_FIELD_NUMBER: _ClassVar[int]
    REGISTER_ENDPOINT_FIELD_NUMBER: _ClassVar[int]
    OPEN_STREAM_FIELD_NUMBER: _ClassVar[int]
    STREAM_OPENED_FIELD_NUMBER: _ClassVar[int]
    STREAM_DATA_FIELD_NUMBER: _ClassVar[int]
    STREAM_CLOSE_FIELD_NUMBER: _ClassVar[int]
    ACK_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    CALL_JOIN_FIELD_NUMBER: _ClassVar[int]
    CALL_ANSWER_FIELD_NUMBER: _ClassVar[int]
    CALL_SIGNAL_FIELD_NUMBER: _ClassVar[int]
    CALL_LEAVE_FIELD_NUMBER: _ClassVar[int]
    CALL_LAYER_FIELD_NUMBER: _ClassVar[int]
    CALL_UPDATE_FIELD_NUMBER: _ClassVar[int]
    CALL_STATE_FIELD_NUMBER: _ClassVar[int]
    START_FIELD_NUMBER: _ClassVar[int]
    FANOUT_AUTH_FIELD_NUMBER: _ClassVar[int]
    MODERATION_FIELD_NUMBER: _ClassVar[int]
    MODERATION_RESULT_FIELD_NUMBER: _ClassVar[int]
    CALL_LISTEN_FIELD_NUMBER: _ClassVar[int]
    CALL_WATCHERS_FIELD_NUMBER: _ClassVar[int]
    hello: Hello
    subscribe: Subscribe
    unsubscribe: Unsubscribe
    publish: Publish
    deliver: Deliver
    register_endpoint: RegisterEndpoint
    open_stream: OpenStream
    stream_opened: StreamOpened
    stream_data: StreamData
    stream_close: StreamClose
    ack: Ack
    error: Error
    call_join: _call_pb2.CallJoin
    call_answer: _call_pb2.CallAnswer
    call_signal: _call_pb2.CallSignal
    call_leave: _call_pb2.CallLeave
    call_layer: _call_pb2.CallLayer
    call_update: _call_pb2.CallUpdate
    call_state: _call_pb2.CallState
    start: Start
    fanout_auth: FanoutAuth
    moderation: RelayModeration
    moderation_result: ModerationResult
    call_listen: _call_pb2.CallListen
    call_watchers: _call_pb2.CallWatchers
    def __init__(self, hello: _Optional[_Union[Hello, _Mapping]] = ..., subscribe: _Optional[_Union[Subscribe, _Mapping]] = ..., unsubscribe: _Optional[_Union[Unsubscribe, _Mapping]] = ..., publish: _Optional[_Union[Publish, _Mapping]] = ..., deliver: _Optional[_Union[Deliver, _Mapping]] = ..., register_endpoint: _Optional[_Union[RegisterEndpoint, _Mapping]] = ..., open_stream: _Optional[_Union[OpenStream, _Mapping]] = ..., stream_opened: _Optional[_Union[StreamOpened, _Mapping]] = ..., stream_data: _Optional[_Union[StreamData, _Mapping]] = ..., stream_close: _Optional[_Union[StreamClose, _Mapping]] = ..., ack: _Optional[_Union[Ack, _Mapping]] = ..., error: _Optional[_Union[Error, _Mapping]] = ..., call_join: _Optional[_Union[_call_pb2.CallJoin, _Mapping]] = ..., call_answer: _Optional[_Union[_call_pb2.CallAnswer, _Mapping]] = ..., call_signal: _Optional[_Union[_call_pb2.CallSignal, _Mapping]] = ..., call_leave: _Optional[_Union[_call_pb2.CallLeave, _Mapping]] = ..., call_layer: _Optional[_Union[_call_pb2.CallLayer, _Mapping]] = ..., call_update: _Optional[_Union[_call_pb2.CallUpdate, _Mapping]] = ..., call_state: _Optional[_Union[_call_pb2.CallState, _Mapping]] = ..., start: _Optional[_Union[Start, _Mapping]] = ..., fanout_auth: _Optional[_Union[FanoutAuth, _Mapping]] = ..., moderation: _Optional[_Union[RelayModeration, _Mapping]] = ..., moderation_result: _Optional[_Union[ModerationResult, _Mapping]] = ..., call_listen: _Optional[_Union[_call_pb2.CallListen, _Mapping]] = ..., call_watchers: _Optional[_Union[_call_pb2.CallWatchers, _Mapping]] = ...) -> None: ...

class Hello(_message.Message):
    __slots__ = ("nonce", "max_chunk", "max_message", "region", "calls", "versions", "pow_bits", "start_window_ms", "cache_ttl_ms", "fanout_threshold", "dedicated_to", "relay_key", "url", "relay_signature")
    NONCE_FIELD_NUMBER: _ClassVar[int]
    MAX_CHUNK_FIELD_NUMBER: _ClassVar[int]
    MAX_MESSAGE_FIELD_NUMBER: _ClassVar[int]
    REGION_FIELD_NUMBER: _ClassVar[int]
    CALLS_FIELD_NUMBER: _ClassVar[int]
    VERSIONS_FIELD_NUMBER: _ClassVar[int]
    POW_BITS_FIELD_NUMBER: _ClassVar[int]
    START_WINDOW_MS_FIELD_NUMBER: _ClassVar[int]
    CACHE_TTL_MS_FIELD_NUMBER: _ClassVar[int]
    FANOUT_THRESHOLD_FIELD_NUMBER: _ClassVar[int]
    DEDICATED_TO_FIELD_NUMBER: _ClassVar[int]
    RELAY_KEY_FIELD_NUMBER: _ClassVar[int]
    URL_FIELD_NUMBER: _ClassVar[int]
    RELAY_SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    nonce: bytes
    max_chunk: int
    max_message: int
    region: str
    calls: bool
    versions: _containers.RepeatedScalarFieldContainer[int]
    pow_bits: int
    start_window_ms: int
    cache_ttl_ms: int
    fanout_threshold: int
    dedicated_to: bytes
    relay_key: bytes
    url: str
    relay_signature: bytes
    def __init__(self, nonce: _Optional[bytes] = ..., max_chunk: _Optional[int] = ..., max_message: _Optional[int] = ..., region: _Optional[str] = ..., calls: _Optional[bool] = ..., versions: _Optional[_Iterable[int]] = ..., pow_bits: _Optional[int] = ..., start_window_ms: _Optional[int] = ..., cache_ttl_ms: _Optional[int] = ..., fanout_threshold: _Optional[int] = ..., dedicated_to: _Optional[bytes] = ..., relay_key: _Optional[bytes] = ..., url: _Optional[str] = ..., relay_signature: _Optional[bytes] = ...) -> None: ...

class RelayBinding(_message.Message):
    __slots__ = ("relay_key", "url", "nonce")
    RELAY_KEY_FIELD_NUMBER: _ClassVar[int]
    URL_FIELD_NUMBER: _ClassVar[int]
    NONCE_FIELD_NUMBER: _ClassVar[int]
    relay_key: bytes
    url: str
    nonce: bytes
    def __init__(self, relay_key: _Optional[bytes] = ..., url: _Optional[str] = ..., nonce: _Optional[bytes] = ...) -> None: ...

class Start(_message.Message):
    __slots__ = ("version", "pow", "req")
    VERSION_FIELD_NUMBER: _ClassVar[int]
    POW_FIELD_NUMBER: _ClassVar[int]
    REQ_FIELD_NUMBER: _ClassVar[int]
    version: int
    pow: bytes
    req: int
    def __init__(self, version: _Optional[int] = ..., pow: _Optional[bytes] = ..., req: _Optional[int] = ...) -> None: ...

class FanoutToken(_message.Message):
    __slots__ = ("issuer", "relay_nonce", "expires_at", "relay_key", "url")
    ISSUER_FIELD_NUMBER: _ClassVar[int]
    RELAY_NONCE_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    RELAY_KEY_FIELD_NUMBER: _ClassVar[int]
    URL_FIELD_NUMBER: _ClassVar[int]
    issuer: bytes
    relay_nonce: bytes
    expires_at: int
    relay_key: bytes
    url: str
    def __init__(self, issuer: _Optional[bytes] = ..., relay_nonce: _Optional[bytes] = ..., expires_at: _Optional[int] = ..., relay_key: _Optional[bytes] = ..., url: _Optional[str] = ...) -> None: ...

class SignedFanoutToken(_message.Message):
    __slots__ = ("token", "signature")
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    token: bytes
    signature: bytes
    def __init__(self, token: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class FanoutAuth(_message.Message):
    __slots__ = ("token", "req")
    TOKEN_FIELD_NUMBER: _ClassVar[int]
    REQ_FIELD_NUMBER: _ClassVar[int]
    token: SignedFanoutToken
    req: int
    def __init__(self, token: _Optional[_Union[SignedFanoutToken, _Mapping]] = ..., req: _Optional[int] = ...) -> None: ...

class ModerationCommand(_message.Message):
    __slots__ = ("server_key", "relay_nonce", "command_id", "expires_at", "block", "unblock", "relay_key", "url")
    SERVER_KEY_FIELD_NUMBER: _ClassVar[int]
    RELAY_NONCE_FIELD_NUMBER: _ClassVar[int]
    COMMAND_ID_FIELD_NUMBER: _ClassVar[int]
    EXPIRES_AT_FIELD_NUMBER: _ClassVar[int]
    BLOCK_FIELD_NUMBER: _ClassVar[int]
    UNBLOCK_FIELD_NUMBER: _ClassVar[int]
    RELAY_KEY_FIELD_NUMBER: _ClassVar[int]
    URL_FIELD_NUMBER: _ClassVar[int]
    server_key: bytes
    relay_nonce: bytes
    command_id: bytes
    expires_at: int
    block: NetworkBlock
    unblock: NetworkUnblock
    relay_key: bytes
    url: str
    def __init__(self, server_key: _Optional[bytes] = ..., relay_nonce: _Optional[bytes] = ..., command_id: _Optional[bytes] = ..., expires_at: _Optional[int] = ..., block: _Optional[_Union[NetworkBlock, _Mapping]] = ..., unblock: _Optional[_Union[NetworkUnblock, _Mapping]] = ..., relay_key: _Optional[bytes] = ..., url: _Optional[str] = ...) -> None: ...

class NetworkBlock(_message.Message):
    __slots__ = ("stream_ids", "seconds")
    STREAM_IDS_FIELD_NUMBER: _ClassVar[int]
    SECONDS_FIELD_NUMBER: _ClassVar[int]
    stream_ids: _containers.RepeatedScalarFieldContainer[int]
    seconds: int
    def __init__(self, stream_ids: _Optional[_Iterable[int]] = ..., seconds: _Optional[int] = ...) -> None: ...

class NetworkUnblock(_message.Message):
    __slots__ = ("block_id",)
    BLOCK_ID_FIELD_NUMBER: _ClassVar[int]
    block_id: bytes
    def __init__(self, block_id: _Optional[bytes] = ...) -> None: ...

class SignedModerationCommand(_message.Message):
    __slots__ = ("command", "signature")
    COMMAND_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    command: bytes
    signature: bytes
    def __init__(self, command: _Optional[bytes] = ..., signature: _Optional[bytes] = ...) -> None: ...

class RelayModeration(_message.Message):
    __slots__ = ("command", "req")
    COMMAND_FIELD_NUMBER: _ClassVar[int]
    REQ_FIELD_NUMBER: _ClassVar[int]
    command: SignedModerationCommand
    req: int
    def __init__(self, command: _Optional[_Union[SignedModerationCommand, _Mapping]] = ..., req: _Optional[int] = ...) -> None: ...

class ModerationResult(_message.Message):
    __slots__ = ("req", "block_id", "networks", "closed", "skipped", "not_found")
    REQ_FIELD_NUMBER: _ClassVar[int]
    BLOCK_ID_FIELD_NUMBER: _ClassVar[int]
    NETWORKS_FIELD_NUMBER: _ClassVar[int]
    CLOSED_FIELD_NUMBER: _ClassVar[int]
    SKIPPED_FIELD_NUMBER: _ClassVar[int]
    NOT_FOUND_FIELD_NUMBER: _ClassVar[int]
    req: int
    block_id: bytes
    networks: int
    closed: int
    skipped: int
    not_found: bool
    def __init__(self, req: _Optional[int] = ..., block_id: _Optional[bytes] = ..., networks: _Optional[int] = ..., closed: _Optional[int] = ..., skipped: _Optional[int] = ..., not_found: _Optional[bool] = ...) -> None: ...

class Subscribe(_message.Message):
    __slots__ = ("topic", "req", "replay_ms")
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    REQ_FIELD_NUMBER: _ClassVar[int]
    REPLAY_MS_FIELD_NUMBER: _ClassVar[int]
    topic: bytes
    req: int
    replay_ms: int
    def __init__(self, topic: _Optional[bytes] = ..., req: _Optional[int] = ..., replay_ms: _Optional[int] = ...) -> None: ...

class Unsubscribe(_message.Message):
    __slots__ = ("topic", "req")
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    REQ_FIELD_NUMBER: _ClassVar[int]
    topic: bytes
    req: int
    def __init__(self, topic: _Optional[bytes] = ..., req: _Optional[int] = ...) -> None: ...

class Publish(_message.Message):
    __slots__ = ("topic", "message_id", "seq", "final", "data", "cacheable")
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    FINAL_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    CACHEABLE_FIELD_NUMBER: _ClassVar[int]
    topic: bytes
    message_id: int
    seq: int
    final: bool
    data: bytes
    cacheable: bool
    def __init__(self, topic: _Optional[bytes] = ..., message_id: _Optional[int] = ..., seq: _Optional[int] = ..., final: _Optional[bool] = ..., data: _Optional[bytes] = ..., cacheable: _Optional[bool] = ...) -> None: ...

class Deliver(_message.Message):
    __slots__ = ("topic", "sender", "message_id", "seq", "final", "data", "replayed")
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    SENDER_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_ID_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    FINAL_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    REPLAYED_FIELD_NUMBER: _ClassVar[int]
    topic: bytes
    sender: int
    message_id: int
    seq: int
    final: bool
    data: bytes
    replayed: bool
    def __init__(self, topic: _Optional[bytes] = ..., sender: _Optional[int] = ..., message_id: _Optional[int] = ..., seq: _Optional[int] = ..., final: _Optional[bool] = ..., data: _Optional[bytes] = ..., replayed: _Optional[bool] = ...) -> None: ...

class RegisterEndpoint(_message.Message):
    __slots__ = ("endpoint_key", "signature", "req", "url")
    ENDPOINT_KEY_FIELD_NUMBER: _ClassVar[int]
    SIGNATURE_FIELD_NUMBER: _ClassVar[int]
    REQ_FIELD_NUMBER: _ClassVar[int]
    URL_FIELD_NUMBER: _ClassVar[int]
    endpoint_key: bytes
    signature: bytes
    req: int
    url: str
    def __init__(self, endpoint_key: _Optional[bytes] = ..., signature: _Optional[bytes] = ..., req: _Optional[int] = ..., url: _Optional[str] = ...) -> None: ...

class OpenStream(_message.Message):
    __slots__ = ("endpoint_key", "stream_id")
    ENDPOINT_KEY_FIELD_NUMBER: _ClassVar[int]
    STREAM_ID_FIELD_NUMBER: _ClassVar[int]
    endpoint_key: bytes
    stream_id: int
    def __init__(self, endpoint_key: _Optional[bytes] = ..., stream_id: _Optional[int] = ...) -> None: ...

class StreamOpened(_message.Message):
    __slots__ = ("stream_id", "endpoint_key")
    STREAM_ID_FIELD_NUMBER: _ClassVar[int]
    ENDPOINT_KEY_FIELD_NUMBER: _ClassVar[int]
    stream_id: int
    endpoint_key: bytes
    def __init__(self, stream_id: _Optional[int] = ..., endpoint_key: _Optional[bytes] = ...) -> None: ...

class StreamData(_message.Message):
    __slots__ = ("stream_id", "data")
    STREAM_ID_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    stream_id: int
    data: bytes
    def __init__(self, stream_id: _Optional[int] = ..., data: _Optional[bytes] = ...) -> None: ...

class StreamClose(_message.Message):
    __slots__ = ("stream_id", "reason")
    STREAM_ID_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    stream_id: int
    reason: str
    def __init__(self, stream_id: _Optional[int] = ..., reason: _Optional[str] = ...) -> None: ...

class Ack(_message.Message):
    __slots__ = ("req",)
    REQ_FIELD_NUMBER: _ClassVar[int]
    req: int
    def __init__(self, req: _Optional[int] = ...) -> None: ...

class Error(_message.Message):
    __slots__ = ("req", "code", "message")
    REQ_FIELD_NUMBER: _ClassVar[int]
    CODE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    req: int
    code: ErrorCode
    message: str
    def __init__(self, req: _Optional[int] = ..., code: _Optional[_Union[ErrorCode, str]] = ..., message: _Optional[str] = ...) -> None: ...
