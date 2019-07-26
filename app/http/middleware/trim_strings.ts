import { Middleware, Request, Response } from '../../../../framework/http.ts'

/**
 * Implements the TrimStrings middleware.
 *
 * @export
 * @class TrimStrings
 * @implements {Middleware}
 */
export class TrimStrings extends Middleware {

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
     * Runs before the route handler.
     *
     * @param {Request} request
     * @memberof Log
     */
    before(request: Request) {
        // ...
    }

    /**
     * Runs after the route handler.
     *
     * @param {Request} request
     * @param {Response} response
     * @memberof Log
     */
    after(request: Request, response: Response) {
        // ...
    }

}
