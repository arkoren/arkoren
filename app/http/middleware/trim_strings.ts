import { Middleware, Handler, Request, Response } from '../../../../arkoren/http.ts'

/**
 * Implements the TrimStrings middleware.
 *
 * @export
 * @class TrimStrings
 * @implements {Middleware}
 */
export default class TrimStrings extends Middleware {

    /**
     * The names of the attributes that should not be trimmed.
     *
     * @protected
     * @type {string[]}
     * @memberof TrimStrings
     */
    protected except: string[] = [
        'password',
        'password_confirmation',
    ]

    /**
     * Handles the middleware code.
     *
     * @param {Request} request
     * @param {Handler} next
     * @returns {Response}
     * @memberof TrimStrings
     */
    handle(request: Request, next: Handler): Response {
        // ...
        return next(request)
    }

}
