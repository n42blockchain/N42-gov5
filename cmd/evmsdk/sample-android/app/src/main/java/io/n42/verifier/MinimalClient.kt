// SPDX-License-Identifier: LGPL-3.0-or-later
//
// MinimalClient — Kotlin binding for the MOBILE MINIMAL stateless client facade
// (cmd/evmsdk/mobile_minimal.go), exposed by gomobile as Evmsdk.mobileMinimal*.
//
//   ① header chain  — mobileMinimalSync(To): extend parentHash to tip.
//   ③ per-account   — mobileBalanceOf: bounded EIP-1186 proof (~KB) verified
//                      against the header-chain-trusted stateRoot.
//
// Facade calls return "" on success / JSON on query / "error: ..." on failure.

package io.n42.verifier

import evmsdk.Evmsdk
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.withContext
import org.json.JSONObject

class MinimalClient {

    data class State(
        val status: String = "Idle",
        val initialized: Boolean = false,
        val head: Long = 0,
        val headHash: String = "",
        val trustBlock: Long = 0,
        val retained: Int = 0,
        val reanchors: Int = 0,
        val syncing: Boolean = false,
        val syncProgress: Float = 0f,
        val lastProofBytes: Int = 0,
        val lastVerify: String = "",
    )

    data class Account(
        val address: String,
        val exists: Boolean,
        val balanceWei: String,
        val nonce: Long,
        val block: Long,
        val proofBytes: Int,
        val verified: Boolean,
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state

    private val _accounts = MutableStateFlow<List<Account>>(emptyList())
    val accounts: StateFlow<List<Account>> = _accounts

    private var tip: Long = 0

    /** Trust a social checkpoint {block, hash} and follow idcURL. */
    fun initialize(idcURL: String, checkpointBlock: Long, checkpointHash: String, retention: Long = 7200) {
        val err = Evmsdk.mobileMinimalInit(idcURL, checkpointBlock, checkpointHash, retention)
        if (err.isNotEmpty()) { _state.value = _state.value.copy(status = "Init failed: $err", initialized = false); return }
        _state.value = _state.value.copy(initialized = true, status = "Anchored @ $checkpointBlock")
        apply(Evmsdk.mobileMinimalState())
    }

    /** Chunked catch-up to tip so the UI animates and the phone never does one huge sync. */
    suspend fun sync(chunk: Long = 2048L) = withContext(Dispatchers.IO) {
        val s = _state.value
        if (!s.initialized || s.syncing) return@withContext
        _state.value = s.copy(syncing = true, status = "Syncing ①")
        val start = _state.value.head
        var target = start
        try {
            while (true) {
                target += chunk
                val js = Evmsdk.mobileMinimalSyncTo(target)
                if (js.startsWith("error:")) { _state.value = _state.value.copy(status = js); return@withContext }
                apply(js)
                if (tip > start) {
                    val p = (_state.value.head - start).toFloat() / (tip - start).coerceAtLeast(1)
                    _state.value = _state.value.copy(syncProgress = p.coerceIn(0f, 1f))
                }
                if (_state.value.head < target) break   // reached the real tip
            }
            apply(Evmsdk.mobileMinimalSync())
            _state.value = _state.value.copy(syncProgress = 1f, status = "At tip")
        } finally {
            _state.value = _state.value.copy(syncing = false)
        }
    }

    /** Trustless balance (③): fetch a bounded EIP-1186 proof + verify vs trusted root. */
    fun verifyBalance(address: String) {
        val js = Evmsdk.mobileBalanceOf(address)
        if (js.startsWith("error:")) { _state.value = _state.value.copy(status = js); return }
        val d = JSONObject(js)
        val acc = Account(
            address = d.optString("address", address),
            exists = d.optBoolean("exists", false),
            balanceWei = d.optString("balance", "0"),
            nonce = d.optLong("nonce", 0),
            block = d.optLong("block", 0),
            proofBytes = d.optInt("proofBytes", 0),
            verified = d.optBoolean("verified", false),
        )
        _state.value = _state.value.copy(lastProofBytes = acc.proofBytes)
        _accounts.value = listOf(acc) + _accounts.value.filter { it.address.lowercase() != acc.address.lowercase() }
    }

    /** Layer ②: on-device witness EVM replay for a block. */
    fun verifyBlock(n: Long) {
        val js = Evmsdk.mobileVerifyBlock(n)
        if (js.startsWith("error:")) { _state.value = _state.value.copy(lastVerify = "② $n: $js"); return }
        val d = JSONObject(js)
        val ok = d.optBoolean("verified", false)
        val tx = d.optInt("txCount", 0)
        _state.value = _state.value.copy(lastVerify = "② block $n: ${if (ok) "✓ verified" else "✗ failed"} · $tx tx")
    }

    fun free() { Evmsdk.mobileMinimalFree(); _state.value = _state.value.copy(initialized = false) }

    private fun apply(js: String) {
        if (js.startsWith("error:")) { _state.value = _state.value.copy(status = js); return }
        val d = JSONObject(js)
        val tr = d.optJSONObject("trustRoot")
        val head = d.optLong("head", _state.value.head)
        if (head > tip) tip = head
        _state.value = _state.value.copy(
            head = head,
            headHash = d.optString("hash", _state.value.headHash),
            retained = d.optInt("retained", _state.value.retained),
            reanchors = d.optInt("reanchors", _state.value.reanchors),
            trustBlock = tr?.optLong("block", _state.value.trustBlock) ?: _state.value.trustBlock,
        )
    }
}
